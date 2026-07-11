package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"game-search-backend/internal/models"
)

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"Halo 3 (USA) (Disc 1)":      "Halo 3",
		"Gears of War 2":             "Gears of War 2",
		"  Resident Evil 4 (Europe)": "Resident Evil 4",
		"game.zip":                   "game",
		"Title.7z":                   "Title",
		"Plain":                      "Plain",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPickBest(t *testing.T) {
	results := []RawgGame{
		{ID: 1, Name: "Halo 3", Platforms: []struct {
			Platform struct {
				Slug string `json:"slug"`
			} `json:"platform"`
		}{{Platform: struct {
			Slug string `json:"slug"`
		}{Slug: "xbox360"}}}},
		{ID: 2, Name: "Halo 3: ODST", Platforms: []struct {
			Platform struct {
				Slug string `json:"slug"`
			} `json:"platform"`
		}{{Platform: struct {
			Slug string `json:"slug"`
		}{Slug: "xbox360"}}}},
		{ID: 3, Name: "Halo 3", Platforms: []struct {
			Platform struct {
				Slug string `json:"slug"`
			} `json:"platform"`
		}{{Platform: struct {
			Slug string `json:"slug"`
		}{Slug: "pc"}}}},
	}
	best := pickBest(results, "halo 3", "xbox360")
	if best == nil || best.ID != 1 {
		t.Errorf("pickBest exact-on-platform = %+v, want ID 1", best)
	}
	// No exact match and no title-similar result: return nil, not the first
	// on-platform result. The old "first on-platform" fallback matched the
	// wrong game for titles RAWG lacks (the EA Sports UFC 5 -> FC 24 bug).
	best = pickBest(results, "something else", "pc")
	if best != nil {
		t.Errorf("pickBest no-credible-match = %+v, want nil (not first pc result)", best)
	}
	// No platform constraint: first exact wins.
	best = pickBest(results, "halo 3", "")
	if best == nil || best.ID != 1 {
		t.Errorf("pickBest no-platform = %+v, want ID 1", best)
	}
	if pickBest(nil, "x", "y") != nil {
		t.Error("pickBest(nil) should return nil")
	}
}

// TestPickBestJaccardFloor grounds the wrong-match rejection: a title RAWG
// lacks must NOT match a loosely-related search result. The Jaccard floor on
// stopword-filtered tokens rejects "EA Sports UFC 5" -> "EA Sports FC 24"
// (shares only the generic "sports") and "Story of Seasons: A Wonderful Life"
// -> "LIVE A LIVE" (no shared tokens), while still accepting legit fuzzy
// matches like "Syberia Remastered" -> "Syberia" (stopword "remastered"
// drops out, tokens match).
func TestPickBestJaccardFloor(t *testing.T) {
	ps5 := plat("playstation5")
	cases := []struct {
		name    string
		want    string
		results []RawgGame
		wantID  int // 0 = expect nil
	}{
		{
			name:    "ufc5 vs fc24 rejected",
			want:    "EA Sports UFC 5",
			results: []RawgGame{{ID: 1, Name: "EA Sports FC 24", Platforms: ps5}},
			wantID:  0,
		},
		{
			name:    "story of seasons vs live a live rejected",
			want:    "STORY OF SEASONS A Wonderful Life",
			results: []RawgGame{{ID: 2, Name: "LIVE A LIVE", Platforms: ps5}},
			wantID:  0,
		},
		{
			name:    "like a dragon gaiden vs infinite wealth rejected",
			want:    "Like A Dragon Gaiden The Man Who Erased His Name",
			results: []RawgGame{{ID: 3, Name: "Like a Dragon Infinite Wealth", Platforms: ps5}},
			wantID:  0,
		},
		{
			name:    "syberia remastered vs syberia accepted",
			want:    "Syberia Remastered",
			results: []RawgGame{{ID: 4, Name: "Syberia", Platforms: ps5}},
			wantID:  4,
		},
		{
			name: "bud spencer punctuated name accepted",
			want: "Bud Spencer Terence Hill Slaps And Beans 2",
			results: []RawgGame{{ID: 5, Name: "Bud Spencer & Terence Hill: Slaps and Beans 2", Platforms: ps5}},
			wantID: 5,
		},
		{
			// Generic-name collision: a chicken game by "The Behemoth" studio,
			// whose RAWG name is the single token "Behemoth", hits Jaccard 0.5 on
			// the shared noun but is a different game. The shared-token guard
			// (want has >=2 distinctive tokens -> require >=2 shared) rejects it.
			name:    "skydances behemoth vs single-token behemoth rejected",
			want:    "Skydances BEHEMOTH",
			results: []RawgGame{{ID: 6, Name: "Behemoth", Platforms: ps5}},
			wantID:  0,
		},
	}
	for _, c := range cases {
		best := pickBest(c.results, c.want, "playstation5")
		if c.wantID == 0 && best != nil {
			t.Errorf("%s: got %+v, want nil (wrong match rejected)", c.name, best)
		}
		if c.wantID != 0 && (best == nil || best.ID != c.wantID) {
			t.Errorf("%s: got %+v, want ID %d", c.name, best, c.wantID)
		}
	}
}

// plat builds a single-platform Platforms slice matching RawgGame's anonymous
// struct, kept here so the Jaccard-floor cases stay readable.
func plat(slug string) []struct {
	Platform struct {
		Slug string `json:"slug"`
	} `json:"platform"`
} {
	return []struct {
		Platform struct {
			Slug string `json:"slug"`
		} `json:"platform"`
	}{{Platform: struct {
		Slug string `json:"slug"`
	}{Slug: slug}}}
}

func TestPlatformToRawgSlug(t *testing.T) {
	want := map[string]string{
		"xbox360": "xbox360",
		"psvita":  "ps-vita",
		"switch":  "nintendo-switch",
		"":        "",
	}
	for in, w := range want {
		if got := PlatformToRawgSlug(in); got != w {
			t.Errorf("PlatformToRawgSlug(%q) = %q, want %q", in, got, w)
		}
	}
}

func TestDescriptionPlaintext(t *testing.T) {
	cases := []struct {
		raw, html, want string
	}{
		{"Plain text.", "<p>ignored</p>", "Plain text."},            // raw preferred
		{"", "<p>Hello <b>world</b></p>", "Hello world"},             // html fallback stripped
		{"", "", ""},                                                 // both empty
		{"", "  <a href=\"x\">link</a> end  ", "link end"},            // whitespace collapsed
	}
	for _, c := range cases {
		if got := descriptionPlaintext(c.raw, c.html); got != c.want {
			t.Errorf("descriptionPlaintext(%q,%q) = %q, want %q", c.raw, c.html, got, c.want)
		}
	}
}

func TestNameList(t *testing.T) {
	devs := []rawgName{{Name: "Bungie"}, {Name: ""}, {Name: "343 Industries"}}
	got := nameList(devs)
	want := []string{"Bungie", "343 Industries"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("nameList = %v, want %v", got, want)
	}
	if nameList(nil) != nil {
		t.Errorf("nameList(nil) should be nil")
	}
}

func TestScreenshotURLs(t *testing.T) {
	shots := []struct {
		Image string `json:"image"`
	}{{Image: "a.jpg"}, {Image: ""}, {Image: "b.jpg"}}
	got := screenshotURLs(shots)
	if len(got) != 2 || got[0] != "a.jpg" || got[1] != "b.jpg" {
		t.Errorf("screenshotURLs = %v, want [a.jpg b.jpg]", got)
	}
}

func TestStoreLinks(t *testing.T) {
	stores := []struct {
		URL   string `json:"url"`
		Store struct {
			Name string `json:"name"`
		} `json:"store"`
	}{
		{URL: "https://store.steampowered.com/app/1", Store: struct {
			Name string `json:"name"`
		}{Name: "Steam"}},
		{URL: "", Store: struct {
			Name string `json:"name"`
		}{}}, // dropped: both empty
	}
	got := storeLinks(stores)
	if len(got) != 1 || got[0].Name != "Steam" || got[0].URL != "https://store.steampowered.com/app/1" {
		t.Errorf("storeLinks = %+v, want one Steam entry", got)
	}
}

// TestEnrichGameNoMatchTerminal verifies the no-match path returns terminal
// fields (DetailFetched=true) so SetGameEnrichment writes rawg_detail_fetched=
// true and the widened missing-query stops re-selecting the title every run.
// Without it, un-matchable titles loop forever (search 200 with 0 results ->
// empty RawgFields -> enriched_rawg=true but detail_fetched=false -> re-query).
// Stubs RAWG /games with an empty results array (the real signature of a title
// RAWG doesn't catalog); no /games/{id} call is made on this path.
func TestEnrichGameNoMatchTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// RAWG returns 200 with an empty results array when the search matches
		// nothing - not a 404, not an error. This is the loop-bug trigger.
		w.Write([]byte(`{"count":0,"results":[]}`))
	}))
	defer srv.Close()

	e := NewEnricher(NewRawgClient(srv.URL, []string{"k"}))
	f, err := e.EnrichGame(context.Background(), models.Game{Title: "Immunuzzle", Platform: models.PlatformSwitch})
	if err != nil {
		t.Fatalf("EnrichGame no-match returned err: %v", err)
	}
	if !f.DetailFetched {
		t.Errorf("no-match DetailFetched = false, want true (terminal); fields=%+v", f)
	}
	// Display fields stay empty for an alternate-source one-off to fill later.
	if f.Cover != "" || f.Description != "" || f.ID != 0 {
		t.Errorf("no-match should leave display fields empty, got %+v", f)
	}
}