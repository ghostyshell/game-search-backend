package jobs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"game-search-backend/internal/models"
	"game-search-backend/internal/services/debrid"
	"game-search-backend/internal/services/metadata"
	"game-search-backend/internal/services/scraper"
	"game-search-backend/pkg/storage"
)

// Runner aggregates the per-source ingest + sync paths. Both the Scheduler
// (periodic) and the one-shot cmd/*ingest binaries call the same Runner.*Sync
// methods so bulk-fill and scheduled sync share one code path.
type Runner struct {
	DB        storage.Database
	Scrapers  *scraper.Service
	HostSyncer *debrid.HostSyncer
	// Enricher enriches games with RAWG (+ future TheGamesDB). Optional; if nil,
	// MetaEnrich is a no-op.
	Enricher *metadata.Enricher
	// Hosts is the cached host -> providers map, refreshed by DebridHostsSync
	// and read by ingest jobs to tag links. ponytail: in-memory cache; rebuilt
	// on each sync. A read-through DB lookup would be cleaner but v1 keeps it
	// in memory + persisted for the API.
	hosts map[string][]models.DebridProvider
}

// NewRunner wires a Runner.
func NewRunner(db storage.Database, svc *scraper.Service, hs *debrid.HostSyncer) *Runner {
	return &Runner{DB: db, Scrapers: svc, HostSyncer: hs}
}

// DebridHostsSync fetches the live TorBox + Real-Debrid host lists, persists
// them, and refreshes the in-memory cache used to tag links.
func (r *Runner) DebridHostsSync(ctx context.Context) error {
	hosts, err := r.HostSyncer.FetchHosts(ctx)
	if err != nil {
		return err
	}
	if err := r.DB.SetDebridHosts(ctx, hosts); err != nil {
		return fmt.Errorf("persist debrid hosts: %w", err)
	}
	r.hosts = hosts
	log.Printf("debrid hosts sync: %d hosts", len(hosts))
	return nil
}

// tagLinks stamps each link's SupportedBy from the cached host map. Torrent
// links are always supported by both providers. Links supported by neither
// are kept (SupportedBy empty) for now; the drop filter is a Phase 4 refinement
// once host coverage is verified.
func (r *Runner) tagLinks(links []models.GameLink) []models.GameLink {
	for i := range links {
		if links[i].LinkType == models.LinkTypeMagnet || links[i].LinkType == models.LinkTypeTorrent {
			links[i].SupportedBy = []models.DebridProvider{models.ProviderTorBox, models.ProviderRealDebrid}
			continue
		}
		host := debrid.NormHost(links[i].Host)
		if providers, ok := r.hosts[host]; ok {
			links[i].SupportedBy = providers
		}
	}
	return links
}

// MinervaSync scrapes Minerva for Xbox 360 + PS3 and upserts games + links.
// Used by both the periodic MinervaSync job and cmd/minervaingest.
func (r *Runner) MinervaSync(ctx context.Context) error {
	for _, p := range []models.Platform{models.PlatformXbox360, models.PlatformPS3} {
		if err := r.ingestPlatform(ctx, p, "minerva sync"); err != nil {
			return err
		}
	}
	return nil
}

// RRomsSync scrapes r-roms for PS2 + PSP + PSVita and upserts games + links.
// Used by both the periodic RRomsSync job and cmd/rromsingest.
func (r *Runner) RRomsSync(ctx context.Context) error {
	for _, p := range []models.Platform{models.PlatformPS2, models.PlatformPSP, models.PlatformPSVita} {
		if err := r.ingestPlatform(ctx, p, "rroms sync"); err != nil {
			return err
		}
	}
	return nil
}

// FitGirlSync scrapes FitGirl for PC across all index pages and upserts games +
// links. FitGirl is a multi-page source (unlike Minerva/r-roms which are
// single-page), so it can't use ingestPlatform (which only scrapes page 1).
// Used by both the periodic FitGirlSync job and cmd/fitgirlingest.
//
// ponytail: upsert per index page so a long WAN ingest is resumable and a
// mid-run failure doesn't lose everything. The page loop terminates when a
// page returns no links (404 past the last page).
func (r *Runner) FitGirlSync(ctx context.Context) error {
	if r.hosts == nil {
		hosts, err := r.DB.GetDebridHosts(ctx)
		if err == nil {
			r.hosts = hosts
		}
	}
	const maxPages = 1000
	for page := 1; page <= maxPages; page++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		links, errs := r.Scrapers.BrowseAll(ctx, models.PlatformPC, page)
		for _, e := range errs {
			log.Printf("fitgirl sync: scraper %s: %v", e.Source, e.Err)
		}
		if len(links) == 0 {
			// Distinguish a genuine end-of-pages (no links, no errors) from a
			// transient scrape failure (no links + errors). The latter must
			// surface as an error so the scheduler / one-shot ingest doesn't
			// treat a failed page 1 as a successful empty ingest.
			if len(errs) > 0 {
				return fmt.Errorf("fitgirl sync: page %d: %w", page, errs[0].Err)
			}
			log.Printf("fitgirl sync: page %d: no links, stopping", page)
			return nil
		}
		links = r.tagLinks(links)
		if err := r.ingestLinks(ctx, models.PlatformPC, "fitgirl sync", links); err != nil {
			return err
		}
	}
	log.Printf("fitgirl sync: hit max page cap %d", maxPages)
	return nil
}

// DLPsGameSync scrapes dlpsgame.com for PS3/PS4/PS5 across all category
// index pages and upserts games + links. It calls the DLPsGame scraper directly
// (not BrowseAll) because PS3 overlaps with Minerva, and upserts per
// page per platform so the long WAN ingest is resumable. Used by both the
// periodic DLPsGameSync job and cmd/dlpsgameingest. PS2 is NOT covered here -
// dlpsgame.com PS2 posts use a plain-body link structure (no base64
// data-payload, dead sister pages) that this 3-hop scraper doesn't parse, and
// the category is niche gravure content; PS2 is owned by RRomsSync per the
// source plan and is already populated.
func (r *Runner) DLPsGameSync(ctx context.Context) error {
	sc, ok := r.Scrapers.ScraperByName(models.SourceDLPsGame)
	if !ok {
		log.Printf("dlpsgame sync: scraper not registered, skipping")
		return nil
	}
	if r.hosts == nil {
		hosts, err := r.DB.GetDebridHosts(ctx)
		if err == nil {
			r.hosts = hosts
		}
	}
	platforms := []models.Platform{models.PlatformPS3, models.PlatformPS4, models.PlatformPS5}
	// ponytail: env-gated platform filter for the one-shot cmd/dlpsgameingest so
	// a targeted re-run can fill PS5 without re-scraping PS4's ~260 pages (which
	// would otherwise re-walk all prior platforms before reaching the missed
	// one and re-hit the 4h ctx deadline). Empty / unset = all three (the
	// scheduled job's behavior). e.g. DLPSGAME_PLATFORMS=ps5.
	if v := strings.TrimSpace(os.Getenv("DLPSGAME_PLATFORMS")); v != "" {
		platforms = filterDLPsGamePlatforms(v, platforms)
	}
	const maxPages = 1000
	var errs []error
	for _, p := range platforms {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for page := 1; page <= maxPages; page++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			links, err := sc.Browse(ctx, p, page)
			if err != nil {
				// Log + break this platform's page loop, then continue to the
				// next platform so a transient 5xx / rate-limit on one platform
				// doesn't starve the other three. Per-page upserts mean progress
				// is saved; re-run is idempotent. Errors are joined + returned.
				log.Printf("dlpsgame sync [%s] page %d: %v, stopping platform", p, page, err)
				errs = append(errs, fmt.Errorf("[%s] page %d: %w", p, page, err))
				break
			}
			if len(links) == 0 {
				log.Printf("dlpsgame sync [%s] page %d: no links, stopping", p, page)
				break
			}
			links = r.tagLinks(links)
			if err := r.ingestLinks(ctx, p, "dlpsgame sync", links); err != nil {
				log.Printf("dlpsgame sync [%s] page %d: ingest: %v, stopping platform", p, page, err)
				errs = append(errs, fmt.Errorf("[%s] page %d ingest: %w", p, page, err))
				break
			}
		}
	}
	return errors.Join(errs...)
}

// filterDLPsGamePlatforms narrows the default 4-platform list to the
// comma-separated names in want (order preserved from the default list, not
// the env, so PS3/PS4/PS5 order is stable). Unknown names are dropped with
// a log line so a typo doesn't silently run nothing.
func filterDLPsGamePlatforms(want string, all []models.Platform) []models.Platform {
	wantSet := make(map[string]bool)
	for _, w := range strings.Split(want, ",") {
		if s := strings.TrimSpace(strings.ToLower(w)); s != "" {
			wantSet[s] = true
		}
	}
	var out []models.Platform
	for _, p := range all {
		if wantSet[string(p)] {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		log.Printf("dlpsgame sync: DLPSGAME_PLATFORMS=%q matched no platform; running all four", want)
		return all
	}
	dropped := make([]string, 0)
	for w := range wantSet {
		matched := false
		for _, p := range all {
			if string(p) == w {
				matched = true
				break
			}
		}
		if !matched {
			dropped = append(dropped, w)
		}
	}
	if len(dropped) > 0 {
		log.Printf("dlpsgame sync: DLPSGAME_PLATFORMS dropped unknown: %s", strings.Join(dropped, ","))
	}
	return out
}

// NSWGameSync scrapes nswgame.com for Switch across all category index pages
// and upserts games + links. Like FitGirl it's multi-page, so it can't use
// ingestPlatform; it loops category pages and upserts per page so the long WAN
// ingest is resumable. Used by both the periodic job and cmd/nswgameingest.
func (r *Runner) NSWGameSync(ctx context.Context) error {
	if r.hosts == nil {
		hosts, err := r.DB.GetDebridHosts(ctx)
		if err == nil {
			r.hosts = hosts
		}
	}
	const maxPages = 1000
	for page := 1; page <= maxPages; page++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		links, errs := r.Scrapers.BrowseAll(ctx, models.PlatformSwitch, page)
		for _, e := range errs {
			log.Printf("nswgame sync: scraper %s: %v", e.Source, e.Err)
		}
		if len(links) == 0 {
			if len(errs) > 0 {
				return fmt.Errorf("nswgame sync: page %d: %w", page, errs[0].Err)
			}
			log.Printf("nswgame sync: page %d: no links, stopping", page)
			return nil
		}
		links = r.tagLinks(links)
		if err := r.ingestLinks(ctx, models.PlatformSwitch, "nswgame sync", links); err != nil {
			return err
		}
	}
	log.Printf("nswgame sync: hit max page cap %d", maxPages)
	return nil
}

// ingestPlatform is the shared scrape -> group-by-slug -> upsert-games + upsert
// -links path. Both MinervaSync and RRomsSync call it so bulk-fill and scheduled
// sync share one code path. name labels the log lines.
func (r *Runner) ingestPlatform(ctx context.Context, platform models.Platform, name string) error {
	if r.hosts == nil {
		// Lazy-load the host cache from the DB so a one-shot ingest tags correctly
		// even if DebridHostsSync hasn't run this session.
		hosts, err := r.DB.GetDebridHosts(ctx)
		if err == nil {
			r.hosts = hosts
		}
	}
	links, errs := r.Scrapers.BrowseAll(ctx, platform, 1)
	for _, e := range errs {
		log.Printf("%s: scraper %s: %v", name, e.Source, e.Err)
	}
	links = r.tagLinks(links)
	if len(links) == 0 {
		// Surface a transient scrape failure as an error rather than a silent
		// empty ingest (0 links + errors = failure; 0 links + no errors = empty
		// source, not a failure).
		if len(errs) > 0 {
			return fmt.Errorf("%s [%s]: %w", name, platform, errs[0].Err)
		}
		log.Printf("%s [%s]: no links scraped", name, platform)
		return nil
	}
	return r.ingestLinks(ctx, platform, name, links)
}

// ingestLinks is the shared group-by-slug -> bulk-upsert-games + upsert-links
// path. Both ingestPlatform (single-page sources) and FitGirlSync (per index
// page) call it so bulk-fill and scheduled sync share one code path.
func (r *Runner) ingestLinks(ctx context.Context, platform models.Platform, name string, links []models.GameLink) error {
	// Defense-in-depth scheme gate: drop any link whose URL is not http/https/
	// magnet before persist. Scrapers only emit these, but a future source or a
	// regex drift could let a javascript:/data: URL through; the safe client
	// validates at fetch time, this guards the persist path.
	filtered := links[:0]
	for _, l := range links {
		if !allowedLinkScheme(l.URL) {
			log.Printf("%s [%s]: dropping link %s with disallowed URL scheme", name, platform, l.ID)
			continue
		}
		filtered = append(filtered, l)
	}
	links = filtered
	if len(links) == 0 {
		log.Printf("%s [%s]: no links after scheme filter", name, platform)
		return nil
	}
	// Group links by game slug; derive title + source from the first link of
	// each slug, then bulk-upsert all games in one chunked call (one WAN
	// round-trip per chunk instead of per game - far less exposed to a mid-run
	// connection reset over the shared Sliplane Mongo).
	bySlug := make(map[string]int)
	for _, l := range links {
		bySlug[l.GameSlug]++
	}
	games := make([]models.Game, 0, len(bySlug))
	now := time.Now().Unix()
	for slug, count := range bySlug {
		var title string
		var src models.Source
		var pubAt int64
		for _, l := range links {
			if l.GameSlug == slug {
				title = l.Title
				src = l.Source
				pubAt = l.SourcePublishedAt
				break
			}
		}
		// Sort key: source post publish date when the scraper captured it
		// (dlpsgame), else scrape wallclock so non-dlpsgame games keep the prior
		// "newest scrape first" behavior under the unified browse sort.
		if pubAt == 0 {
			pubAt = now
		}
		games = append(games, models.Game{
			Slug:              slug,
			Title:             title,
			Platform:          platform,
			Source:            src,
			LinkCount:         count,
			SourcePublishedAt: pubAt,
		})
	}
	if err := r.DB.UpsertGames(ctx, games); err != nil {
		return fmt.Errorf("%s: upsert games: %w", name, err)
	}
	if err := r.DB.UpsertGameLinks(ctx, links); err != nil {
		return fmt.Errorf("%s: upsert game links: %w", name, err)
	}
	log.Printf("%s [%s]: %d links, %d games", name, platform, len(links), len(bySlug))
	return nil
}

// allowedLinkScheme reports whether a link URL is http(s) or a magnet URI.
// Used as a defense-in-depth gate before persisting scraped links.
func allowedLinkScheme(raw string) bool {
	if raw == "" {
		return false
	}
	if strings.HasPrefix(raw, "magnet:") {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// Client returns a fresh safe HTTP client (used by cmd/*ingest helpers).
func (r *Runner) Client() *http.Client { return r.Scrapers.Client() }

// MetaEnrich enriches games that lack RAWG metadata, in batches, until the
// pool is drained or all RAWG keys are exhausted. On ErrAllKeysExhausted it
// stops cleanly and logs the ask-for-a-new-key marker. Idempotent: re-runs
// skip games already marked enriched_rawg=true.
//
// ponytail: always fetch offset 0 - both successful enrichments and no-match
// games leave the unenriched set, so it strictly shrinks. Advancing offset
// would skip unenriched games as the set shrinks. A persistent-error game
// would loop forever, so we break when a batch makes zero progress.
func (r *Runner) MetaEnrich(ctx context.Context) error {
	if r.Enricher == nil {
		log.Printf("meta enrich: no enricher configured, skipping")
		return nil
	}
	const batch = 100
	total := 0
	for {
		games, err := r.DB.GetGamesMissingRawgEnrich(ctx, batch, 0)
		if err != nil {
			return fmt.Errorf("fetch missing-enrich games: %w", err)
		}
		if len(games) == 0 {
			log.Printf("meta enrich: done, enriched %d games this run", total)
			return nil
		}
		progress := 0
		for _, g := range games {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fields, err := r.Enricher.EnrichGame(ctx, g)
			if err != nil {
				if err == metadata.ErrAllKeysExhausted {
					log.Printf("meta enrich: RAWG: all API keys exhausted. Provide a new key (append to RAWG_API_KEYS) and re-run. Enriched %d games before exhaustion.", total)
					return err
				}
				log.Printf("meta enrich: %s (%s): %v", g.Slug, g.Title, err)
				continue
			}
			if err := r.DB.SetGameEnrichment(ctx, g.Slug, fields); err != nil {
				log.Printf("meta enrich: persist %s: %v", g.Slug, err)
				continue
			}
			total++
			progress++
			if total%100 == 0 {
				log.Printf("meta enrich: %d games enriched", total)
			}
		}
		if progress == 0 {
			// Every game in the batch errored; re-fetching would loop forever.
			log.Printf("meta enrich: stalled - batch of %d all errored, stopping. Enriched %d this run.", len(games), total)
			return nil
		}
	}
}