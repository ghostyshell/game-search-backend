package scraper

import (
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"game-search-backend/internal/models"
)

// NSWGameScraper scrapes nswgame.com (Nintendo Switch). It is a two-level
// scraper: the category index (/category/switch/page/N/) lists per-game posts;
// each per-game post has DDL mirrors on filehosts (megaup.net, 1fichier.com,
// send.now, gofile.io, datanodes.to, ...) as plain <a> links. Grounded against
// the live WordPress HTML on 2026-07-01.
//
// ponytail: one DDL GameLink per distinct filehost per game (deduped by host).
// The category index is filtered to game pages by slug - every game slug
// contains "nintendo-switch", nav/admin slugs (list-all, daily-update, dmca,
// ...) do not. No HTML parser; regex over flat <a> tags (matches the other
// scrapers, zero deps).
type NSWGameScraper struct {
	client *http.Client
}

func NewNSWGameScraper(client *http.Client) *NSWGameScraper {
	if client == nil {
		client = NewSafeClient(30 * time.Second)
	}
	return &NSWGameScraper{client: client}
}

func (s *NSWGameScraper) Name() models.Source { return models.SourceNSWGame }

func (s *NSWGameScraper) Platforms() []models.Platform { return []models.Platform{models.PlatformSwitch} }

// nswGameBase is the site root. Overridable for tests.
var nswGameBase = "https://nswgame.com"

const nswGameFetchSem = 6

// nswGameIndexRe matches a category-index <a> linking to a per-game post. We
// capture href + link text; the host is checked against nswGameBase and the
// slug is filtered to game pages (contains "nintendo-switch") in code, so the
// regex is host-agnostic and the scraper is testable against a stub.
var nswGameIndexRe = regexp.MustCompile(`<a\s+href="([^"]+)"[^>]*>([^<]+)</a>`)

// nswGameLinkRe matches any external https <a> href; the host is checked
// against the nav/sibling blocklist to keep only filehost download mirrors.
var nswGameLinkRe = regexp.MustCompile(`href="(https?://([a-zA-Z0-9.-]+)/[^"]+)"`)

// nswGameNavHosts are hosts whose <a> links on a game page are navigation,
// social, sibling sites, or tool pages - not filehost downloads. Any external
// host not in this set is treated as a download mirror. ponytail: blocklist
// rather than an allowlist so new filehosts are picked up automatically; the
// debrid host tagging + Phase 4 drop filter handle non-filehost false positives.
var nswGameNavHosts = map[string]bool{
	"nswgame.com": true, "www.nswgame.com": true,
	"dlpsgame.com": true, "downloadgamepsp.org": true, "downloadgameps3.net": true,
	"downloadgameps3.com": true, "downloadgamexbox.com": true, "gamepciso.com": true,
	"www.blogger.com": true, "schema.org": true, "s.w.org": true, "api.w.org": true,
	"gmpg.org": true, "fontawesome.com": true, "use.fontawesome.com": true,
	"fonts.googleapis.com": true, "fonts.gstatic.com": true, "yoast.com": true,
	"kokoanalytics.com": true, "ajdg.solutions": true, "al5sm.com": true,
	"www.googletagmanager.com": true, "www.google.com": true, "google.com": true,
	"www.youtube.com": true, "youtube.com": true, "facebook.com": true,
	"x.com": true, "twitter.com": true, "t.me": true, "telegram.me": true,
	"www.reddit.com": true, "reddit.com": true, "discord.gg": true,
	"discord.com": true, "instagram.com": true, "www.instagram.com": true,
	"pinterest.com": true, "tumblr.com": true, "vk.com": true,
}

// Browse scrapes category index `page` (1-indexed): fetches it, then each
// per-game post on that page, and returns per-host DDL links per game.
// Returns nil when page is out of range (404 / no games) so a page loop ends.
func (s *NSWGameScraper) Browse(ctx context.Context, platform models.Platform, page int) ([]models.GameLink, error) {
	if platform != models.PlatformSwitch {
		return nil, nil
	}
	if page < 1 {
		page = 1
	}
	indexURL := nswGameBase + "/category/switch/page/" + fmt.Sprintf("%d", page) + "/"
	body, status, err := s.fetchURLStatus(ctx, indexURL)
	if err != nil {
		return nil, fmt.Errorf("nswgame index %s: %w", indexURL, err)
	}
	if status == http.StatusNotFound || status == http.StatusGone {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, nil
	}

	type post struct {
		url   string
		title string
	}
	baseHost := hostOf(nswGameBase)
	var posts []post
	seenSlug := make(map[string]struct{})
	for _, m := range nswGameIndexRe.FindAllSubmatch(body, -1) {
		href := string(m[1])
		text := string(m[2])
		if hostOf(href) != baseHost {
			continue
		}
		slug := nswGameSlugFromURL(href)
		title := cleanText(html.UnescapeString(text))
		// Game pages are the only slugs containing "nintendo-switch"; nav/admin
		// pages (list-all, daily-update, dmca, warning, tag, category, ...) do not.
		if !strings.Contains(slug, "nintendo-switch") || title == "" {
			continue
		}
		if _, dup := seenSlug[slug]; dup {
			continue
		}
		seenSlug[slug] = struct{}{}
		posts = append(posts, post{url: href, title: title})
	}
	if len(posts) == 0 {
		return nil, nil
	}
	// Cap per-page post count before fanning out goroutines (see fitgirl.go).
	const nswGameMaxPostsPerPage = 200
	if len(posts) > nswGameMaxPostsPerPage {
		log.Printf("nswgame: index page lists %d posts, capping to %d", len(posts), nswGameMaxPostsPerPage)
		posts = posts[:nswGameMaxPostsPerPage]
	}

	type pageLinks struct {
		idx   int
		links []models.GameLink
	}
	results := make([]pageLinks, len(posts))
	sem := make(chan struct{}, nswGameFetchSem)
	var wg sync.WaitGroup
	for i, p := range posts {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int, p post) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = pageLinks{idx: i, links: s.scrapePost(ctx, p.url, p.title)}
		}(i, p)
	}
	wg.Wait()
	var links []models.GameLink
	for _, r := range results {
		links = append(links, r.links...)
	}
	return links, nil
}

// scrapePost fetches one per-game post and emits one DDL GameLink per distinct
// external (non-nav) filehost. Skips posts with no mirrors.
func (s *NSWGameScraper) scrapePost(ctx context.Context, postURL, title string) []models.GameLink {
	body, status, err := s.fetchURLStatus(ctx, postURL)
	if err != nil || status != http.StatusOK {
		return nil
	}
	now := time.Now().Unix()
	slug := nswGameSlugFromURL(postURL)
	if slug == "" {
		return nil
	}
	// Source post publish date (Yoast article:published_time). The category
	// index sorts by this date; carrying it on every link lets ingestLinks set
	// it as the Game's sort key so Switch browse order matches nswgame.com.
	pubAt := articlePublishedAt(body)
	seenHost := make(map[string]struct{})
	var links []models.GameLink
	for _, m := range nswGameLinkRe.FindAllSubmatch(body, -1) {
		host := strings.ToLower(string(m[2]))
		if host == "" || nswGameNavHosts[host] {
			continue
		}
		if _, dup := seenHost[host]; dup {
			continue
		}
		seenHost[host] = struct{}{}
		links = append(links, models.GameLink{
			ID:       linkID(models.SourceNSWGame, "ddl:"+slug+":"+host),
			GameSlug: slug,
			Platform: models.PlatformSwitch,
			Source:   models.SourceNSWGame,
			Title:    title,
			Host:     host,
			URL:      string(m[1]),
			LinkType: models.LinkTypeDDL,
			// SupportedBy filled by tagLinks from the cached host map.
			SourcePublishedAt: pubAt,
			UpdatedAt:         now,
			CreatedAt:         now,
		})
	}
	return links
}

func (s *NSWGameScraper) fetchURLStatus(ctx context.Context, u string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// nswGameSlugFromURL extracts the post slug from a per-game post URL.
func nswGameSlugFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	seg := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(seg) == 0 || seg[len(seg)-1] == "" {
		return ""
	}
	return seg[len(seg)-1]
}

// hostOf returns the host of a URL ("" on parse error).
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}