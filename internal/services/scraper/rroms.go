package scraper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"game-search-backend/internal/models"
)

// RRomsScraper scrapes r-roms.github.io Sony pages (PS2, PSP, PSVita). Each page
// is a table whose rows link to archive.org collection items; the scraper
// enumerates each collection's original files via the IA Metadata API and emits
// one DDL GameLink per ROM file. Grounded against the live SvelteKit-rendered
// HTML (server-rendered <tr data-slot="table-row"> with archive.org/download/
// hrefs) on 2026-07-01.
type RRomsScraper struct {
	client *http.Client
}

func NewRRomsScraper(client *http.Client) *RRomsScraper {
	if client == nil {
		client = NewSafeClient(30 * time.Second)
	}
	return &RRomsScraper{client: client}
}

func (s *RRomsScraper) Name() models.Source { return models.SourceRRoms }

func (s *RRomsScraper) Platforms() []models.Platform {
	return []models.Platform{models.PlatformPS2, models.PlatformPSP, models.PlatformPSVita}
}

// rromsPageURL maps a Sony platform to its r-roms megathread page.
var rromsPageURL = map[models.Platform]string{
	models.PlatformPS2:    "https://r-roms.github.io/Sony/sony-playstation-2",
	models.PlatformPSP:    "https://r-roms.github.io/Sony/sony-playstation-portable",
	models.PlatformPSVita: "https://r-roms.github.io/Sony/sony-playstation-vita",
}

func (s *RRomsScraper) Browse(ctx context.Context, platform models.Platform, page int) ([]models.GameLink, error) {
	pageURL, ok := rromsPageURL[platform]
	if !ok {
		return nil, nil
	}
	// ponytail: r-roms pages are not paginated; page > 1 returns nothing so
	// BrowseAll's page loop terminates after page 1.
	if page > 1 {
		return nil, nil
	}
	itemIDs, err := s.fetchIAItemIDs(ctx, pageURL)
	if err != nil {
		return nil, fmt.Errorf("r-roms %s: %w", platform, err)
	}
	if len(itemIDs) == 0 {
		return nil, nil
	}
	var links []models.GameLink
	for _, id := range itemIDs {
		if ctx.Err() != nil {
			return links, ctx.Err()
		}
		files, err := FetchIAFiles(ctx, s.client, id)
		if err != nil {
			// one failed collection shouldn't kill the whole platform scrape
			continue
		}
		links = append(links, IAGameLinks(platform, models.SourceRRoms, id, files)...)
	}
	return links, nil
}

// rromsIAHrefRe matches archive.org/download/<id> hrefs in the page HTML. The
// id segment stops at the next "/" or quote. ponytail: regex over a full HTML
// parser (goquery) because the structure is flat <a href="..."> tags and regex
// keeps the dependency list at zero (matches the Minerva scraper's approach).
var rromsIAHrefRe = regexp.MustCompile(`href="(https?://archive\.org/download/[^"/?#" ]+)[^"]*"`)

// fetchIAItemIDs fetches the r-roms page and extracts deduped archive.org item
// IDs from any /download/<id> hrefs.
func (s *RRomsScraper) fetchIAItemIDs(ctx context.Context, pageURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var ids []string
	seen := make(map[string]struct{})
	for _, m := range rromsIAHrefRe.FindAllSubmatch(body, -1) {
		id := iaItemIDFromURL(string(m[1]))
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

// iaItemIDFromURL extracts the archive.org item id from a /download/<id>[/<file>]
// URL. Returns "" if not a valid download URL.
func iaItemIDFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/download/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil {
		return ""
	}
	return id
}