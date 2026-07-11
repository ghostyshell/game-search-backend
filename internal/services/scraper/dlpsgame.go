package scraper

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"game-search-backend/internal/models"
)

// DLPsGameScraper scrapes dlpsgame.com (PS3/PS4/PS5). It is a three-level
// scraper: the category index (/category/<cat>/page/N/) lists per-game posts;
// each post hides its real links in a Shortcodes Ultimate "secure-data" div
// whose data-payload attribute is base64-encoded HTML; the decoded HTML points
// at a sister site (downloadgameps3.net/archives/<id>) whose static page holds
// the plain filehost <a href> download mirrors (1fichier, akirabox, buzzheavier,
// rootz, mediafire, filekeeper, datavaults, ...). Some payloads also carry
// direct filehost hrefs (no sister hop). Grounded against the live WordPress
// HTML on 2026-07-02.
//
// The site wraps filehost URLs in an AdLinkFly shortener (clk.sh) loaded by a
// Dean Edwards packed script, but that rewrite happens client-side in the
// browser only; the sister-site hrefs are the raw filehost URLs in the static
// HTML, so a server scraper never needs to execute JS or run an adblock. The
// "JS is just ads" intuition is correct - adblock is unnecessary server-side.
//
// ponytail: one DDL GameLink per distinct filehost per game (deduped by host),
// reusing the fitgirl/nswgame bounded-concurrency + 200-post fan-out cap. No
// HTML parser; regex over flat <a> tags (zero deps, matches the other scrapers).
type DLPsGameScraper struct {
	client *http.Client
}

func NewDLPsGameScraper(client *http.Client) *DLPsGameScraper {
	if client == nil {
		client = NewSafeClient(30 * time.Second)
	}
	return &DLPsGameScraper{client: client}
}

func (s *DLPsGameScraper) Name() models.Source { return models.SourceDLPsGame }

func (s *DLPsGameScraper) Platforms() []models.Platform {
	return []models.Platform{models.PlatformPS3, models.PlatformPS4, models.PlatformPS5}
}

// dlpsGameBase is the site root. Overridable for tests (set to httptest URL).
var dlpsGameBase = "https://dlpsgame.com"

// dlpsGameFetchSem bounds concurrent per-game fetches within one index page.
// Lower than the 2-hop scrapers (6) because the 3-hop scraper issues ~1 post +
// up to 16 sister fetches per game, so the same fan-out is ~5x more fetches on
// the site; 4 keeps the burst below dlpsgame.com's rate limit (with the 429
// backoff in fetchURLStatus as the safety net).
const dlpsGameFetchSem = 4

// dlpsGameMaxSisterPerPost caps the number of sister-site archive URLs fetched
// per post. A crafted post could pack thousands of archive URLs into a base64
// payload; each fetch has a 30s timeout so one bad post could stall a worker
// goroutine for hours. Real outlier posts (big multi-part games) legitimately
// carry 17-26 archive pages (Disney Dreamlight Valley 18, Dragon Ball Z
// Kakarot 18, WWE 2K25 21, Street Fighter 6 17, WWE 2K24 26, RDR2 25), so 16
// dropped real mirrors; 32 recovers them while staying bounded (32 x 30s worst
// case per post, real pages are sub-second). Mirrors the dlpsGameMaxPostsPerPage
// fan-out cap at the index level.
const dlpsGameMaxSisterPerPost = 32

// dlpsGameIndexRe matches a category-index <a> linking to a per-game post. We
// capture href + link text; the host is checked against dlpsGameBase and the
// slug is filtered to game pages in code, so the regex is host-agnostic and the
// scraper is testable against a stub. Identical shape to nswGameIndexRe.
var dlpsGameIndexRe = regexp.MustCompile(`<a\s+href="([^"]+)"[^>]*>([^<]+)</a>`)

// dlpsGamePayloadRe matches the base64 data-payload attribute on the
// secure-data div. The decoded value is HTML carrying sister-site + filehost
// hrefs. Multiple payloads per post (one per download variant) are normal.
var dlpsGamePayloadRe = regexp.MustCompile(`data-payload="([A-Za-z0-9+/=]+)"`)

// dlpsGameSisterHost is the sister site that hosts the static filehost hrefs.
// Overridable for tests (point at an httptest server). The decoded payload
// links here as https://<host>/archives/<id>; the scraper fetches that page for
// the final hop to the plain filehost <a href> download mirrors.
var dlpsGameSisterHost = "downloadgameps3.net"

// dlpsGameSisterRe returns the sister-site archive URL regex, built from the
// current dlpsGameSisterHost so tests can redirect the hop at a stub. Recompiled
// per post (once per scrapePost; negligible vs the WAN fetches it gates). A
// package-level var regex would not pick up the test override of dlpsGameSisterHost.
func dlpsGameSisterRe() *regexp.Regexp {
	return regexp.MustCompile(`href="(https?://(?:www\.)?` + regexp.QuoteMeta(dlpsGameSisterHost) + `(?::\d+)?/archives/\d+)"`)
}

// dlpsGameMagnetRe picks up the occasional magnet in a decoded payload.
var dlpsGameMagnetRe = regexp.MustCompile(`(magnet:\?xt=urn:btih:[A-Za-z0-9]+[^"\s<]*)`)

// dlpsGameNavPrefixes are index-page slug prefixes that are NOT game posts
// (admin/index/guide pages). They end in the platform suffix too
// (list-all-game-ps2, list-game-ps5, daily-update-...), so the suffix filter
// alone is not enough; this prefix gate keeps only real game posts.
var dlpsGameNavPrefixes = []string{
	"list-", "daily-", "guide", "warning", "dmca", "tag-", "category",
	"contact", "notice", "notification", "disclaimer", "privacy", "about",
}

// dlpsGameExtraNavHosts are nav/sibling/guide hosts whose <a> hrefs are not
// filehost downloads, on top of the shared nswGameNavHosts blocklist. The
// sister site downloadgameps3.net is handled separately (fetched, never emitted
// directly); downloadgameps3.com is the guide/nav sibling, skipped.
var dlpsGameExtraNavHosts = map[string]bool{
	"dlpsgame.com": true, "www.dlpsgame.com": true,
	"downloadgameps3.net": true, "www.downloadgameps3.net": true,
	"downloadgameps3.com": true, "www.downloadgameps3.com": true,
	"downloadgamepsp.org": true, "www.downloadgamepsp.org": true,
	"downloadgamexbox.com": true,
}

func dlpsGameIsNavHost(host string) bool {
	if dlpsGameExtraNavHosts[host] || nswGameNavHosts[host] {
		return true
	}
	// The sister host is fetched separately for the final hop; a payload href to
	// it is not itself a filehost download. Checked dynamically (not via the nav
	// map) so an overridden test sister host is skipped too.
	return host == dlpsGameSisterHost || host == "www."+dlpsGameSisterHost
}

// Browse scrapes category index `page` (1-indexed) for the platform, then each
// per-game post on that page, and returns per-host DDL links per game.
// Returns nil when page is out of range (404 / no games) so a page loop ends.
func (s *DLPsGameScraper) Browse(ctx context.Context, platform models.Platform, page int) ([]models.GameLink, error) {
	cat := string(platform)
	if !dlpsGameCatSupported(cat) {
		return nil, nil
	}
	if page < 1 {
		page = 1
	}
	indexURL := dlpsGameBase + "/category/" + cat + "/page/" + fmt.Sprintf("%d", page) + "/"
	body, status, err := s.fetchURLStatus(ctx, indexURL)
	if err != nil {
		return nil, fmt.Errorf("dlpsgame index %s: %w", indexURL, err)
	}
	if status == http.StatusNotFound || status == http.StatusGone {
		return nil, nil
	}
	if status != http.StatusOK {
		// A 5xx/rate-limit on the index is a transient outage, NOT end-of-pages.
		// Returning nil here would make DLPsGameSync log "no links, stopping" and
		// silently skip the platform for this run. Surface it as an error so the
		// sync job / one-shot ingest can log + continue to the next platform and
		// the operator sees the outage. 404/gone above is the real end-of-pages.
		return nil, fmt.Errorf("dlpsgame index %s: HTTP %d", indexURL, status)
	}

	type post struct {
		url   string
		title string
	}
	baseHost := hostOf(dlpsGameBase)
	token := "-" + cat + "-"
	var posts []post
	seenSlug := make(map[string]struct{})
	for _, m := range dlpsGameIndexRe.FindAllSubmatch(body, -1) {
		href := string(m[1])
		text := string(m[2])
		if hostOf(href) != baseHost {
			continue
		}
		slug := nswGameSlugFromURL(href)
		title := cleanText(html.UnescapeString(text))
		// Game post slugs contain the platform as a kebab token (-ps4-), but may
		// carry a format suffix after it (jazzpunk-directors-cut-ps4-pkg,
		// ...-ps4-fpkg, ...-ps3-iso). Pad the slug so the token match is
		// boundary-correct, then drop admin/index/guide pages (list-all-game-ps4,
		// list-game-ps5, daily-update, ...) via the prefix gate - those also
		// contain the platform token.
		if title == "" || dlpsGameIsNavSlug(slug) || !strings.Contains("-"+slug+"-", token) {
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
	const dlpsGameMaxPostsPerPage = 200
	if len(posts) > dlpsGameMaxPostsPerPage {
		log.Printf("dlpsgame: index page lists %d posts, capping to %d", len(posts), dlpsGameMaxPostsPerPage)
		posts = posts[:dlpsGameMaxPostsPerPage]
	}

	type pageLinks struct {
		idx   int
		links []models.GameLink
	}
	results := make([]pageLinks, len(posts))
	sem := make(chan struct{}, dlpsGameFetchSem)
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
			results[i] = pageLinks{idx: i, links: s.scrapePost(ctx, p.url, p.title, platform)}
		}(i, p)
	}
	wg.Wait()
	var links []models.GameLink
	for _, r := range results {
		links = append(links, r.links...)
	}
	return links, nil
}

// scrapePost fetches one per-game post, base64-decodes every data-payload,
// collects sister-site archive URLs + any direct filehost hrefs + magnets,
// fetches each sister page for its static filehost hrefs, and emits one DDL
// GameLink per distinct filehost (plus any magnet). Dedup is by host across
// the whole post (direct + sister). Skips posts with no mirrors.
func (s *DLPsGameScraper) scrapePost(ctx context.Context, postURL, title string, platform models.Platform) []models.GameLink {
	body, status, err := s.fetchURLStatus(ctx, postURL)
	if err != nil || status != http.StatusOK {
		return nil
	}
	slug := nswGameSlugFromURL(postURL)
	if slug == "" {
		return nil
	}

	// Decode every secure-data payload into one combined HTML blob.
	var decoded strings.Builder
	for _, m := range dlpsGamePayloadRe.FindAllSubmatch(body, -1) {
		b, derr := base64.StdEncoding.DecodeString(string(m[1]))
		if derr != nil {
			continue
		}
		decoded.Write(b)
	}
	dec := decoded.String()
	if dec == "" {
		return nil
	}

	now := time.Now().Unix()
	pubAt := articlePublishedAt(body)
	var links []models.GameLink

	// Magnet (rare on DLPsGame but cheap to pick up).
	if mm := dlpsGameMagnetRe.FindString(dec); mm != "" {
		magnet := html.UnescapeString(mm)
		if strings.HasPrefix(magnet, "magnet:?xt=urn:btih:") {
			links = append(links, models.GameLink{
				ID:                linkID(models.SourceDLPsGame, "magnet:"+slug),
				GameSlug:          slug,
				Platform:          platform,
				Source:            models.SourceDLPsGame,
				Title:             title,
				Host:              "",
				URL:               magnet,
				LinkType:          models.LinkTypeMagnet,
				SourcePublishedAt: pubAt,
				UpdatedAt:         now,
				CreatedAt:         now,
			})
		}
	}

	// Sister-site archive URLs to fetch for the final hop (deduped by URL).
	seenSister := make(map[string]struct{})
	var sisterURLs []string
	for _, m := range dlpsGameSisterRe().FindAllSubmatch([]byte(dec), -1) {
		u := html.UnescapeString(string(m[1]))
		if _, dup := seenSister[u]; dup {
			continue
		}
		seenSister[u] = struct{}{}
		sisterURLs = append(sisterURLs, u)
	}
	if len(sisterURLs) > dlpsGameMaxSisterPerPost {
		log.Printf("dlpsgame: post %s has %d sister URLs, capping to %d", slug, len(sisterURLs), dlpsGameMaxSisterPerPost)
		sisterURLs = sisterURLs[:dlpsGameMaxSisterPerPost]
	}

	// Direct filehost hrefs already in the payload (skip nav + sister host).
	seenHost := make(map[string]struct{})
	for _, m := range nswGameLinkRe.FindAllSubmatch([]byte(dec), -1) {
		host := strings.ToLower(string(m[2]))
		if host == "" || dlpsGameIsNavHost(host) {
			continue
		}
		if _, dup := seenHost[host]; dup {
			continue
		}
		seenHost[host] = struct{}{}
		links = append(links, models.GameLink{
			ID:                linkID(models.SourceDLPsGame, "ddl:"+slug+":"+host),
			GameSlug:          slug,
			Platform:          platform,
			Source:            models.SourceDLPsGame,
			Title:             title,
			Host:              host,
			URL:               string(m[1]),
			LinkType:          models.LinkTypeDDL,
			SourcePublishedAt: pubAt,
			UpdatedAt:         now,
			CreatedAt:         now,
		})
	}

	// Final hop: each sister page's static filehost hrefs.
	for _, u := range sisterURLs {
		if ctx.Err() != nil {
			break
		}
		sbody, sstatus, serr := s.fetchURLStatus(ctx, u)
		if serr != nil || sstatus != http.StatusOK {
			continue
		}
		for _, m := range nswGameLinkRe.FindAllSubmatch(sbody, -1) {
			host := strings.ToLower(string(m[2]))
			if host == "" || dlpsGameIsNavHost(host) {
				continue
			}
			if _, dup := seenHost[host]; dup {
				continue
			}
			seenHost[host] = struct{}{}
			links = append(links, models.GameLink{
				ID:                linkID(models.SourceDLPsGame, "ddl:"+slug+":"+host),
				GameSlug:          slug,
				Platform:          platform,
				Source:            models.SourceDLPsGame,
				Title:             title,
				Host:              host,
				URL:               string(m[1]),
				LinkType:          models.LinkTypeDDL,
				SourcePublishedAt: pubAt,
				UpdatedAt:         now,
				CreatedAt:         now,
			})
		}
	}

	// Source page: the dlpsgame.com post URL itself, so the UI can link back to
	// the source post (mirrors, screenshots, install notes). Only when the post
	// yielded download links, to keep the "no mirrors -> no game" behavior.
	// Distinct _id ("source:"+slug) from the magnet/DDL keys so no collision.
	if len(links) > 0 {
		links = append(links, models.GameLink{
			ID:                linkID(models.SourceDLPsGame, "source:"+slug),
			GameSlug:          slug,
			Platform:          platform,
			Source:            models.SourceDLPsGame,
			Title:             title,
			Host:              "dlpsgame.com",
			URL:               postURL,
			LinkType:          models.LinkTypeSource,
			SourcePublishedAt: pubAt,
			UpdatedAt:         now,
			CreatedAt:         now,
		})
	}
	return links
}

// fetchURLStatus fetches u with a bounded retry on 429 (rate limit). dlpsgame.com
// 429s under burst scraping; the 3-hop scraper is fetch-heavy (1 post + up to 16
// sister pages per game), so a bulk fill trips the limiter. Retrying with
// increasing backoff lets a background fill progress politely instead of
// aborting a whole platform on the first 429. 5xx are not retried here (surfaced
// immediately so DLPsGameSync can log + continue to the next platform).
func (s *DLPsGameScraper) fetchURLStatus(ctx context.Context, u string) ([]byte, int, error) {
	backoffs := []time.Duration{3 * time.Second, 6 * time.Second, 12 * time.Second}
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, resp.StatusCode, readErr
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < len(backoffs) {
			select {
			case <-time.After(backoffs[attempt]):
			case <-ctx.Done():
				return nil, resp.StatusCode, ctx.Err()
			}
			continue
		}
		return body, resp.StatusCode, nil
	}
}

func dlpsGameCatSupported(cat string) bool {
	switch cat {
	case "ps3", "ps4", "ps5":
		return true
	}
	return false
}

func dlpsGameIsNavSlug(slug string) bool {
	for _, p := range dlpsGameNavPrefixes {
		if strings.HasPrefix(slug, p) {
			return true
		}
	}
	return false
}