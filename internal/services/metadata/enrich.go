package metadata

import (
	"context"
	"regexp"
	"strings"

	"game-search-backend/internal/models"
)

// Enricher coordinates RAWG (+ future TheGamesDB) enrichment.
type Enricher struct {
	rawg *RawgClient
}

// NewEnricher builds an enricher. rawg may be nil / keyless; EnrichGame then
// returns ErrAllKeysExhausted so the caller can stop cleanly.
func NewEnricher(rawg *RawgClient) *Enricher {
	return &Enricher{rawg: rawg}
}

// EnrichGame searches RAWG for the best match for game.Title on game.Platform,
// then fetches /games/{id} for the full metadata. Returns ErrAllKeysExhausted
// when the key pool is empty so the caller can stop and ask for a fresh key.
// On a non-exhausted detail error, returns the error so MetaEnrich skips this
// game and retries it next run (DetailFetched stays false, so the widened
// missing-query re-selects it).
func (e *Enricher) EnrichGame(ctx context.Context, game models.Game) (models.RawgFields, error) {
	if e.rawg == nil || !e.rawg.HasKeys() {
		return models.RawgFields{}, ErrAllKeysExhausted
	}
	want := cleanTitle(game.Title)
	wantPlatform := PlatformToRawgSlug(string(game.Platform))
	results, err := e.rawg.SearchByTitle(ctx, want)
	if err != nil {
		return models.RawgFields{}, err
	}
	best := pickBest(results, want, wantPlatform)
	if best == nil {
		// No RAWG match: RAWG has no entry for this title, so there is no
		// /games/{id} detail to fetch. Mark DetailFetched so the caller's
		// SetGameEnrichment writes rawg_detail_fetched=true and the widened
		// missing-query (enriched_rawg != true OR rawg_detail_fetched != true)
		// stops re-selecting this title every run. Without this, un-matchable
		// titles (obscure Japan/Korea exclusives, eShop indies RAWG doesn't
		// catalog) loop forever - the search returns 200 with 0 results, the
		// run logs nothing, increments its counter, and re-selects the same
		// games next pass, burning key budget indefinitely. The display fields
		// stay empty; an alternate-source one-off (cmd/tgdbenrich) can still
		// populate them later. Verified against the 59 stragglers that stalled
		// the 2026-07 backfill (Gyeoljeon, Iinazuke, Immunuzzle, ... all return
		// 0 RAWG results).
		return models.RawgFields{DetailFetched: true}, nil
	}

	detail, err := e.rawg.GameDetail(ctx, best.ID)
	if err != nil {
		// Search matched but detail failed: surface the error so the run skips
		// this game without marking it fetched. The missing-query re-selects it.
		return models.RawgFields{}, err
	}

	f := models.RawgFields{
		ID:          int64(best.ID),
		Rating:      best.Rating,
		Cover:       best.BackgroundImage,
		Released:    best.Released,
		Genres:      genreNames(best.Genres),
		Screenshots: screenshotURLs(best.ShortScreenshots),
	}
	// Prefer the detail payload where it overlaps (it is authoritative) and
	// carries the fields the list omits.
	if detail != nil {
		// Cover: prefer detail's additional image only when the list lacked one.
		if f.Cover == "" && detail.BackgroundImage != "" {
			f.Cover = detail.BackgroundImage
		}
		if detail.BackgroundImageAdditional != "" {
			f.Screenshots = append([]string{detail.BackgroundImageAdditional}, f.Screenshots...)
		}
		if detail.Rating != 0 {
			f.Rating = detail.Rating
		}
		if detail.Released != "" {
			f.Released = detail.Released
		}
		if len(detail.Genres) > 0 {
			f.Genres = genreNames(detail.Genres)
		}
		f.Description = descriptionPlaintext(detail.DescriptionRaw, detail.Description)
		f.Metacritic = detail.Metacritic
		f.MetacriticURL = detail.MetacriticURL
		if detail.ESRBRating != nil {
			f.ESRB = detail.ESRBRating.Name
		}
		f.Developers = nameList(detail.Developers)
		f.Publishers = nameList(detail.Publishers)
		f.Tags = nameList(detail.Tags)
		f.Stores = storeLinks(detail.Stores)
		f.Playtime = detail.Playtime
		f.RedditURL = detail.RedditURL
		f.DetailFetched = true
	}
	return f, nil
}

// pickBest prefers an exact (case-insensitive) title match on the target
// platform, then any exact title match, then the most title-similar result on
// the target platform, then the most title-similar result overall - but only
// if that similarity clears a Jaccard floor. Without the floor, a title RAWG
// lacks (e.g. "EA Sports UFC 5") matches the first loosely-related search
// result ("EA Sports FC 24") and the wrong game's cover/description get
// stored against the right title/links. Returns nil when no result is
// credibly the same game, so EnrichGame marks the title terminal-empty
// (honest no-data) instead of wrong-match (wrong cover/description).
func pickBest(results []RawgGame, want, wantPlatform string) *RawgGame {
	if len(results) == 0 {
		return nil
	}
	wantLower := strings.ToLower(want)
	wantTokens := titleTokens(want)
	var exactOnPlatform, exactAny *RawgGame
	var bestOnPlat, bestAny *RawgGame
	bestOnPlatJ, bestAnyJ := -1.0, -1.0
	bestOnPlatShared, bestAnyShared := 0, 0
	for i := range results {
		r := &results[i]
		nameLower := strings.ToLower(r.Name)
		onPlat := wantPlatform == "" || hasPlatform(r, wantPlatform)
		if nameLower == wantLower {
			if onPlat && exactOnPlatform == nil {
				exactOnPlatform = r
			}
			if exactAny == nil {
				exactAny = r
			}
			continue
		}
		// Fuzzy: track the highest-similarity result (on-platform preferred at
		// equal clarity). Returns only if it clears the gates below. Track the
		// shared-token count too, since the floor alone admits generic-name
		// collisions (a chicken game named "Behemoth" vs "Skydances BEHEMOTH"
		// hits Jaccard 0.5 on the single shared noun).
		j, shared := jaccardShared(wantTokens, titleTokens(r.Name))
		if onPlat && (j > bestOnPlatJ || (j == bestOnPlatJ && shared > bestOnPlatShared)) {
			bestOnPlat, bestOnPlatJ, bestOnPlatShared = r, j, shared
		}
		if j > bestAnyJ || (j == bestAnyJ && shared > bestAnyShared) {
			bestAny, bestAnyJ, bestAnyShared = r, j, shared
		}
	}
	switch {
	case exactOnPlatform != nil:
		return exactOnPlatform
	case exactAny != nil:
		return exactAny
	}
	// ponytail: 0.5 Jaccard floor on stopword-filtered tokens. Cleanly rejects
	// the wrong-match cases ("EA Sports UFC 5" vs "EA Sports FC 24" = 0.4, shares
	// only "ea"/"sports"; "Story of Seasons: A Wonderful Life" vs "LIVE A LIVE"
	// = 0.0; "Like a Dragon Gaiden..." vs "Like a Dragon: Infinite Wealth" = 0.2)
	// while accepting legit fuzzy matches ("Syberia Remastered" vs "Syberia" =
	// 1.0 after the "remastered" stopword drops; "Bud Spencer... Slaps and Beans
	// 2" vs the punctuated RAWG name ~1.0). Plus a shared-token guard: when the
	// want title has >=2 distinctive tokens, require >=2 shared so a generic
	// 1-token RAWG name can't collide ("Skydances BEHEMOTH" vs a chicken game
	// named "Behemoth" hits 0.5 on the single shared noun). Below the gates,
	// return nil so the title is terminal-empty, not wrong.
	const minJaccard = 0.5
	minShared := 1
	if len(wantTokens) >= 2 {
		minShared = 2
	}
	if bestOnPlat != nil && bestOnPlatJ >= minJaccard && bestOnPlatShared >= minShared {
		return bestOnPlat
	}
	if bestAny != nil && bestAnyJ >= minJaccard && bestAnyShared >= minShared {
		return bestAny
	}
	return nil
}

// titleTokens lowercases s and returns its distinctive tokens: length > 2,
// minus a small stopword set so generic words ("the", "of", "edition") don't
// inflate similarity between unrelated titles that happen to share them.
func titleTokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		// split on any non-alphanumeric (space, hyphen, ampersand, punctuation)
		// so "Bud Spencer & Terence Hill" -> [bud spencer terence hill].
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	}) {
		if len(w) < 2 {
			continue
		}
		if _, skip := titleStopwords[w]; skip {
			continue
		}
		out[w] = struct{}{}
	}
	return out
}

var titleStopwords = map[string]struct{}{
	"the": {}, "and": {}, "of": {}, "for": {}, "in": {}, "on": {}, "to": {},
	"edition": {}, "deluxe": {}, "standard": {}, "collection": {}, "series": {},
	"game": {}, "vol": {}, "part": {}, "remaster": {}, "remastered": {},
}

// jaccard is |A∩B| / |A∪B| over two token sets; 0 for disjoint sets.
func jaccard(a, b map[string]struct{}) float64 {
	j, _ := jaccardShared(a, b)
	return j
}

// jaccardShared returns the Jaccard similarity plus the |A∩B| intersection
// count, so pickBest can gate on both (a high Jaccard from one shared token
// against a 1-token name is a collision, not a match).
func jaccardShared(a, b map[string]struct{}) (float64, int) {
	if len(a) == 0 || len(b) == 0 {
		return 0, 0
	}
	var inter, union int
	for w := range a {
		union++
		if _, ok := b[w]; ok {
			inter++
		}
	}
	for w := range b {
		if _, ok := a[w]; !ok {
			union++
		}
	}
	if union == 0 {
		return 0, 0
	}
	return float64(inter) / float64(union), inter
}

func hasPlatform(r *RawgGame, slug string) bool {
	for _, p := range r.Platforms {
		if p.Platform.Slug == slug {
			return true
		}
	}
	return false
}

func genreNames(genres []struct {
	Name string `json:"name"`
}) []string {
	if len(genres) == 0 {
		return nil
	}
	out := make([]string, 0, len(genres))
	for _, g := range genres {
		if g.Name != "" {
			out = append(out, g.Name)
		}
	}
	return out
}

// rawgName is the shared shape of RAWG's {Name string} slices (developers,
// publishers, tags, genres are structurally identical anonymous structs).
type rawgName = struct {
	Name string `json:"name"`
}

// nameList lifts the Name field from a slice of {Name} structs (developers,
// publishers, tags). Shares the rawgName alias so one helper covers all three.
func nameList(items []rawgName) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it.Name != "" {
			out = append(out, it.Name)
		}
	}
	return out
}

func screenshotURLs(shots []struct {
	Image string `json:"image"`
}) []string {
	if len(shots) == 0 {
		return nil
	}
	out := make([]string, 0, len(shots))
	for _, s := range shots {
		if s.Image != "" {
			out = append(out, s.Image)
		}
	}
	return out
}

func storeLinks(stores []struct {
	URL   string `json:"url"`
	Store struct {
		Name string `json:"name"`
	} `json:"store"`
}) []models.RawgStore {
	if len(stores) == 0 {
		return nil
	}
	out := make([]models.RawgStore, 0, len(stores))
	for _, s := range stores {
		if s.Store.Name == "" && s.URL == "" {
			continue
		}
		out = append(out, models.RawgStore{Name: s.Store.Name, URL: s.URL})
	}
	return out
}

// descriptionPlaintext returns the plaintext description, preferring RAWG's
// description_raw and falling back to HTML-stripped description for older
// entries that don't carry description_raw. No new dep: a regex strips tags.
func descriptionPlaintext(raw, html string) string {
	if raw != "" {
		return raw
	}
	if html == "" {
		return ""
	}
	plain := htmlTags.ReplaceAllString(html, "")
	// Collapse whitespace from the stripped tags.
	plain = wsCollapse.ReplaceAllString(plain, " ")
	return strings.TrimSpace(plain)
}

var (
	htmlTags  = regexp.MustCompile(`<[^>]*>`)
	wsCollapse = regexp.MustCompile(`\s+`)
)

// cleanTitle strips the parenthetical region/disc qualifiers and file
// extensions that source titles carry ("Halo 3 (USA) (Disc 1)" -> "Halo 3"),
// so RAWG keyword search matches cleanly.
func cleanTitle(title string) string {
	t := strings.TrimSpace(title)
	// drop everything from the first " (" to end; source qualifiers are
	// parenthetical and trail the title.
	if i := strings.Index(t, " ("); i >= 0 {
		t = t[:i]
	}
	// ponytail: also strip common archive exts if a bare filename slipped in.
	low := strings.ToLower(t)
	for _, ext := range []string{".zip", ".7z", ".rar", ".iso", ".nsp", ".xex"} {
		if strings.HasSuffix(low, ext) {
			t = t[:len(t)-len(ext)]
			break
		}
	}
	return strings.TrimSpace(t)
}