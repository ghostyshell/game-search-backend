package scraper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"game-search-backend/internal/models"
)

// MinervaScraper scrapes Minerva Archive browse pages for Xbox 360 title files.
// Each file entry becomes one DDL GameLink under a Game titled from the filename.
//
// Source registry mirrors godsend-360's config.go (verified local reference at
// ~/Code/godsend-360): per-platform browse URLs + the href regex
// `href="(/rom\?name=[^"]+)"`. The collection-level .torrent files
// (MinervaTorrentURLs) are surfaced as torrent GameLinks too, so the catalog
// has both per-title DDL and a per-collection torrent (torrents are always
// supported by both providers).
type MinervaScraper struct {
	client *http.Client
}

// NewMinervaScraper builds a Minerva scraper. Pass the Service's shared client
// so SSRF + body-cap defenses apply.
func NewMinervaScraper(client *http.Client) *MinervaScraper {
	if client == nil {
		client = NewSafeClient(30 * time.Second)
	}
	return &MinervaScraper{client: client}
}

func (m *MinervaScraper) Name() models.Source { return models.SourceMinerva }

func (m *MinervaScraper) Platforms() []models.Platform {
	return []models.Platform{models.PlatformXbox360, models.PlatformPS3}
}

// minervaBrowseURLs maps a platform to the Minerva browse page(s) that list
// per-title ROM downloads.
var minervaBrowseURLs = map[models.Platform][]string{
	models.PlatformXbox360: {
		"https://minerva-archive.org/browse/Redump/Microsoft%20-%20Xbox%20360/",
		"https://minerva-archive.org/browse/No-Intro/Non-Redump%20-%20Microsoft%20-%20Xbox%20360/",
	},
	models.PlatformPS3: {
		"https://minerva-archive.org/browse/Redump/Sony%20-%20PlayStation%203/",
	},
}

// minervaCollectionTorrents maps a platform to its collection .torrent URL(s).
// Torrents are always supportedBy both providers.
var minervaCollectionTorrents = map[models.Platform][]string{
	models.PlatformXbox360: {
		"https://minerva-archive.org/assets/Minerva_Myrient_v0.3/Minerva_Myrient%20-%20Redump%20-%20Microsoft%20-%20Xbox%20360.torrent",
		"https://minerva-archive.org/assets/Minerva_Myrient_v0.3/Minerva_Myrient%20-%20No-Intro%20-%20Non-Redump%20-%20Microsoft%20-%20Xbox%20360.torrent",
	},
	models.PlatformPS3: {
		"https://minerva-archive.org/assets/Minerva_Myrient_v0.3/Minerva_Myrient%20-%20Redump%20-%20Sony%20-%20PlayStation%203.torrent",
	},
}

// MinervaHrefRe extracts href="/rom?name=..." from Minerva browse pages.
var MinervaHrefRe = regexp.MustCompile(`href="(/rom\?name=[^"]+)"`)

const minervaHost = "minerva-archive.org"
const minervaDownloadBase = "https://minerva-archive.org"

// Browse scrapes page 1 of the platform's Minerva browse page(s) and returns one
// DDL GameLink per file plus one torrent GameLink per collection .torrent.
// page > 1 currently returns only the collection torrents (browse pagination is
// not exposed by Minerva; ponytail: revisit if a page param appears).
func (m *MinervaScraper) Browse(ctx context.Context, platform models.Platform, page int) ([]models.GameLink, error) {
	if platform != models.PlatformXbox360 && platform != models.PlatformPS3 {
		return nil, nil
	}
	now := time.Now().Unix()
	var links []models.GameLink

	// Collection torrents (always both providers).
	for _, tURL := range minervaCollectionTorrents[platform] {
		title := collectionTitle(tURL)
		links = append(links, models.GameLink{
			ID:          linkID(models.SourceMinerva, tURL),
			GameSlug:    slugify(title),
			Platform:    platform,
			Source:      models.SourceMinerva,
			Title:       title,
			Host:        minervaHost,
			URL:         tURL,
			LinkType:    models.LinkTypeTorrent,
			SupportedBy: []models.DebridProvider{models.ProviderTorBox, models.ProviderRealDebrid},
			UpdatedAt:   now,
			CreatedAt:   now,
		})
	}

	// Per-file DDL from browse pages (page 1 only).
	if page <= 1 {
		for _, browseURL := range minervaBrowseURLs[platform] {
			fileLinks, err := m.scrapeBrowsePage(ctx, browseURL, platform)
			if err != nil {
				return links, fmt.Errorf("minerva browse %s: %w", browseURL, err)
			}
			links = append(links, fileLinks...)
		}
	}
	return links, nil
}

func (m *MinervaScraper) scrapeBrowsePage(ctx context.Context, browseURL string, platform models.Platform) ([]models.GameLink, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, browseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := m.client.Do(req)
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

	matches := MinervaHrefRe.FindAllSubmatch(body, -1)
	now := time.Now().Unix()
	seen := make(map[string]struct{}, len(matches))
	var links []models.GameLink
	for _, mt := range matches {
		hrefVal := string(mt[1])
		const prefix = "/rom?name="
		if !strings.HasPrefix(hrefVal, prefix) {
			continue
		}
		pathParam := hrefVal[len(prefix):]
		decoded, err := url.PathUnescape(pathParam)
		if err != nil {
			continue
		}
		ext := strings.ToLower(filepath.Ext(decoded))
		if ext != ".zip" && ext != ".7z" && ext != ".rar" {
			continue
		}
		fileName := filepath.Base(decoded)
		title := strings.TrimSuffix(fileName, ext)
		slug := slugify(title)
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		links = append(links, models.GameLink{
			ID:          linkID(models.SourceMinerva, pathParam),
			GameSlug:    slug,
			Platform:    platform,
			Source:      models.SourceMinerva,
			Title:       title,
			Host:        minervaHost,
			URL:         minervaDownloadBase + hrefVal,
			LinkType:    models.LinkTypeDDL,
			SupportedBy: nil, // ponytail: host sync fills this; minerva-archive.org eligibility TBD
			UpdatedAt:   now,
			CreatedAt:   now,
		})
	}
	return links, nil
}

func collectionTitle(torrentURL string) string {
	base := filepath.Base(torrentURL)
	base = strings.TrimSuffix(base, ".torrent")
	base = strings.ReplaceAll(base, "%20", " ")
	return base
}