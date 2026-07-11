// Package scraper holds the per-source scrapers and the fan-out Service that
// aggregates them for on-demand browse/search.
package scraper

import (
	"context"
	"net/http"
	"sync"
	"time"

	"game-search-backend/internal/models"
)

// Scraper scrapes one download source. Browse returns GameLinks for a platform
// page (1-indexed); sources with no per-file browse (only collection torrents)
// may return a single torrent link per platform.
type Scraper interface {
	Name() models.Source
	Platforms() []models.Platform
	Browse(ctx context.Context, platform models.Platform, page int) ([]models.GameLink, error)
}

// Service aggregates scrapers and fans out browse/search across all sources
// that support a given platform.
type Service struct {
	client   *http.Client
	scrapers map[models.Source]Scraper
	mu      sync.RWMutex
}

// NewService builds a Service with the SSRF-hardened safe client.
func NewService() *Service {
	return &Service{
		client:   NewSafeClient(30 * time.Second),
		scrapers: make(map[models.Source]Scraper),
	}
}

// RegisterScraper adds a scraper to the registry.
func (s *Service) RegisterScraper(sc Scraper) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scrapers[sc.Name()] = sc
}

// Client exposes the shared safe HTTP client to scrapers.
func (s *Service) Client() *http.Client { return s.client }

// Scrapers returns the registered scrapers (snapshot).
func (s *Service) Scrapers() []Scraper {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Scraper, 0, len(s.scrapers))
	for _, sc := range s.scrapers {
		out = append(out, sc)
	}
	return out
}

// ScraperByName returns a registered scraper by its Source name. Used by sync
// jobs that scrape a source whose platforms overlap other sources (DLPsGame
// shares PS2/PS3 with RRoms/Minerva), so they can't use BrowseAll without
// re-running the peers on every page.
func (s *Service) ScraperByName(source models.Source) (Scraper, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc, ok := s.scrapers[source]
	return sc, ok
}

// BrowseAll fans out Browse across every scraper that supports the platform.
// Errors from one scraper do not abort the others; each scraper's links are
// returned tagged with their source. Page is 1-indexed.
func (s *Service) BrowseAll(ctx context.Context, platform models.Platform, page int) ([]models.GameLink, []ScraperError) {
	s.mu.RLock()
	scraperList := make([]Scraper, 0, len(s.scrapers))
	for _, sc := range s.scrapers {
		if supports(sc, platform) {
			scraperList = append(scraperList, sc)
		}
	}
	s.mu.RUnlock()

	type result struct {
		links []models.GameLink
		err   ScraperError
	}
	resultCh := make(chan result, len(scraperList))
	for _, sc := range scraperList {
		go func(sc Scraper) {
			links, err := sc.Browse(ctx, platform, page)
			// Forward partial links alongside the error: Minerva returns its
			// collection torrents in `links` then a per-browse-page error from
			// scrapeBrowsePage; dropping `links` here would silently lose the
			// torrents on a transient browse-page failure.
			r := result{links: links}
			if err != nil {
				r.err = ScraperError{Source: sc.Name(), Err: err}
			}
			resultCh <- r
		}(sc)
	}

	var allLinks []models.GameLink
	var errs []ScraperError
	for range scraperList {
		r := <-resultCh
		allLinks = append(allLinks, r.links...)
		if r.err.Source != "" {
			errs = append(errs, r.err)
		}
	}
	return allLinks, errs
}

func supports(sc Scraper, platform models.Platform) bool {
	for _, p := range sc.Platforms() {
		if p == platform {
			return true
		}
	}
	return false
}

// ScraperError carries a per-source failure out of BrowseAll without aborting peers.
type ScraperError struct {
	Source models.Source
	Err    error
}

func (e ScraperError) Error() string { return string(e.Source) + ": " + e.Err.Error() }