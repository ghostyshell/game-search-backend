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

// FitGirlScraper scrapes fitgirl-repacks.site (PC). It is a two-level scraper:
// the index (/page/N/) lists per-game posts; each per-game post has one magnet
// plus multi-part filehost DDL mirrors (datanodes.to, fuckingfast.co, ...).
// Grounded against the live WordPress HTML on 2026-07-01.
//
// ponytail: one DDL GameLink per distinct filehost per game (the part1 URL is
// the representative). Multi-part archives need all parts to extract - the
// frontend resolves each; the catalog only needs "this game is mirrored on
// host X". Storing every part would flood the link list with 6+ near-identical
// rows per host per game. The magnet is the primary (whole-game) download.
type FitGirlScraper struct {
	client *http.Client
}

func NewFitGirlScraper(client *http.Client) *FitGirlScraper {
	if client == nil {
		client = NewSafeClient(30 * time.Second)
	}
	return &FitGirlScraper{client: client}
}

func (s *FitGirlScraper) Name() models.Source { return models.SourceFitGirl }

func (s *FitGirlScraper) Platforms() []models.Platform { return []models.Platform{models.PlatformPC} }

// fitGirlBase is the site root. Overridable for tests (set to httptest URL).
var fitGirlBase = "https://fitgirl-repacks.site"

// fitGirlArticleRe matches an index-page article header: the per-game post URL
// and its title. re.S so the title capture spans inline tags.
var fitGirlArticleRe = regexp.MustCompile(`<h1 class="entry-title"><a href="([^"]+)"[^>]*>(.*?)</a></h1>`)

// fitGirlMagnetRe matches the magnet href on a per-game page. The href stores
// '&' as '&#038;'; we HTML-unescape it before storing so the magnet is valid.
var fitGirlMagnetRe = regexp.MustCompile(`href="(magnet:\?xt=urn:btih:[A-Za-z0-9]+[^"]*)"`)

// fitGirlDDLRe matches multi-part filehost archive links. We capture host + URL
// and dedupe by host, keeping the first (part1) per host. Host char class is
// case-insensitive so an uppercase host isn't dropped; the captured host is
// lowercased before dedup + storage.
var fitGirlDDLRe = regexp.MustCompile(`https?://([a-zA-Z0-9.-]+)/[^\s"<>]+\.(?:rar|7z|zip|part\d+\.rar)(?:[/?#][^\s"<>]*)?`)

// fitGirlFetchSem bounds concurrent per-game page fetches within one index page.
// ponytail: stdlib channel semaphore; a real worker pool if throughput demands.
const fitGirlFetchSem = 6

// Browse scrapes index page `page` (1-indexed): fetches it, then each per-game
// post on that page, and returns one magnet + per-host DDL links per game.
// Returns nil when page is out of range (no articles) so a page loop terminates.
func (s *FitGirlScraper) Browse(ctx context.Context, platform models.Platform, page int) ([]models.GameLink, error) {
	if platform != models.PlatformPC {
		return nil, nil
	}
	if page < 1 {
		page = 1
	}
	indexURL := fitGirlBase + "/page/" + fmt.Sprintf("%d", page) + "/"
	body, status, err := s.fetchURLStatus(ctx, indexURL)
	if err != nil {
		return nil, fmt.Errorf("fitgirl index %s: %w", indexURL, err)
	}
	// WordPress returns 404 past the last index page; that's the end-of-pages
	// signal, not an error. Treat 410 the same. Other non-200 already errored.
	if status == http.StatusNotFound || status == http.StatusGone {
		return nil, nil
	}

	type post struct {
		url   string
		title string
	}
	var posts []post
	baseHost := hostOf(fitGirlBase)
	for _, m := range fitGirlArticleRe.FindAllSubmatch(body, -1) {
		href := string(m[1])
		title := cleanText(html.UnescapeString(stripTags(string(m[2]))))
		if href == "" || title == "" {
			continue
		}
		// Defense-in-depth: only follow per-game posts on the same host as
		// fitGirlBase. The safe client's dial layer already refuses non-public
		// IPs, but this keeps a compromised index page from steering fetches at
		// the application layer (matches the NSWGame scraper's host guard).
		if hostOf(href) != baseHost {
			continue
		}
		posts = append(posts, post{url: href, title: title})
	}
	if len(posts) == 0 {
		return nil, nil
	}
	// ponytail: cap the per-page post count before fanning out goroutines.
	// The semaphore bounds concurrent fetches but every post spawns a goroutine
	// that blocks on the sem; a crafted index page packed with 100k articles
	// would spawn 100k goroutines. Real pages have ~10; cap generous + log.
	const fitGirlMaxPostsPerPage = 200
	if len(posts) > fitGirlMaxPostsPerPage {
		log.Printf("fitgirl: index page lists %d posts, capping to %d", len(posts), fitGirlMaxPostsPerPage)
		posts = posts[:fitGirlMaxPostsPerPage]
	}

	links := make([]models.GameLink, 0, len(posts)*2)
	type pageLinks struct {
		idx   int
		links []models.GameLink
	}
	results := make([]pageLinks, len(posts))
	sem := make(chan struct{}, fitGirlFetchSem)
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
	for _, r := range results {
		links = append(links, r.links...)
	}
	return links, nil
}

// scrapePost fetches one per-game post and emits its magnet + per-host DDL links.
// Skips posts with no magnet and no DDL (e.g. "Upcoming Repacks" notice posts).
func (s *FitGirlScraper) scrapePost(ctx context.Context, postURL, title string) []models.GameLink {
	slug := fitGirlSlugFromURL(postURL)
	if slug == "" {
		return nil
	}
	body, err := s.fetchURL(ctx, postURL)
	if err != nil {
		return nil
	}
	now := time.Now().Unix()
	// Source post publish date (Yoast article:published_time). The fitgirl index
	// sorts by this date; carrying it on every link lets ingestLinks set it as
	// the Game's sort key so PC browse order matches fitgirl-repacks.site.
	pubAt := articlePublishedAt(body)
	var links []models.GameLink

	if mm := fitGirlMagnetRe.FindSubmatch(body); mm != nil {
		magnet := html.UnescapeString(string(mm[1]))
		if strings.HasPrefix(magnet, "magnet:?xt=urn:btih:") {
			links = append(links, models.GameLink{
				ID:       linkID(models.SourceFitGirl, "magnet:"+slug),
				GameSlug: slug,
				Platform: models.PlatformPC,
				Source:   models.SourceFitGirl,
				Title:    title,
				Host:     "",
				URL:      magnet,
				LinkType: models.LinkTypeMagnet,
				// SupportedBy filled by tagLinks (magnets -> both providers).
				SourcePublishedAt: pubAt,
				UpdatedAt:         now,
				CreatedAt:         now,
			})
		}
	}

	seenHost := make(map[string]struct{})
	for _, m := range fitGirlDDLRe.FindAllSubmatch(body, -1) {
		host := strings.ToLower(string(m[1]))
		if host == "" {
			continue
		}
		if _, dup := seenHost[host]; dup {
			continue
		}
		seenHost[host] = struct{}{}
		links = append(links, models.GameLink{
			ID:       linkID(models.SourceFitGirl, "ddl:"+slug+":"+host),
			GameSlug: slug,
			Platform: models.PlatformPC,
			Source:   models.SourceFitGirl,
			Title:    title,
			Host:     host,
			URL:      string(m[0]),
			LinkType: models.LinkTypeDDL,
			// SupportedBy filled by tagLinks from the cached host map.
			SourcePublishedAt: pubAt,
			UpdatedAt:         now,
			CreatedAt:         now,
		})
	}
	return links
}

func (s *FitGirlScraper) fetchURL(ctx context.Context, u string) ([]byte, error) {
	body, status, err := s.fetchURLStatus(ctx, u)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", status)
	}
	return body, nil
}

// fetchURLStatus returns body + status. err is non-nil only for transport/read
// failures; HTTP status codes (incl. 404) are returned in status for the caller
// to interpret - 404 on an index page means end-of-pages, not a transport error.
func (s *FitGirlScraper) fetchURLStatus(ctx context.Context, u string) ([]byte, int, error) {
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

// fitGirlSlugFromURL extracts the post slug from a per-game post URL:
// https://fitgirl-repacks.site/blackshard-repack-updated/ -> blackshard-repack-updated.
func fitGirlSlugFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	seg := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(seg) == 0 || seg[len(seg)-1] == "" {
		return ""
	}
	s, err := url.PathUnescape(seg[len(seg)-1])
	if err != nil {
		return ""
	}
	return s
}

// stripTags removes inline HTML tags from a captured title fragment.
func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cleanText collapses whitespace, trims, and normalizes em/en dashes to ASCII
// hyphen. FitGirl titles frequently use &#8211;/&#8212; which html.UnescapeString
// turns into en/em dashes; the catalog surfaces on the frontend, so we keep them
// ASCII per the no-em-dash rule for user-visible copy.
func cleanText(s string) string {
	s = strings.ReplaceAll(s, "—", "-")
	s = strings.ReplaceAll(s, "–", "-")
	s = strings.TrimSpace(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}