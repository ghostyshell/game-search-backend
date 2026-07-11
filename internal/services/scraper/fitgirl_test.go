package scraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"game-search-backend/internal/models"
)

// TestFitGirlBrowse grounds the two-level scrape against fixture WordPress
// HTML served by an httptest stub. The index lists two posts; one per-game post
// has a magnet + multi-part filehost DDL links, the other is a notice (no
// magnet/DDL, skipped). Asserts: one magnet + one DDL per distinct host per
// game, HTML-entity decoding of the magnet, en-dash normalization + slug
// derivation, and that page 2 (404) terminates the page loop.
func TestFitGirlBrowse(t *testing.T) {
	postHTML := `<h1 class="entry-title">BLACKSHARD &#8211; v1.01 + Bonus OST</h1>
<meta property="article:published_time" content="2026-06-09T22:30:01+00:00" />
<a href="magnet:?xt=urn:btih:E7474AC2DDEA20F15416AC3505D2111721565454&#038;dn=BLACKSHARD&#038;tr=udp%3A%2F%2Ftracker.example">magnet</a>
<a href="https://datanodes.to/j9jj0do0a1qk/BLACKSHARD.part1.rar">p1</a>
<a href="https://datanodes.to/1gwxob1u6ypj/BLACKSHARD.part2.rar">p2</a>
<a href="https://fuckingfast.co/abc/BLACKSHARD.part1.rar">ff1</a>`

	noticeHTML := `<h1 class="entry-title">Upcoming Repacks</h1><p>just a notice, no links</p>`

	// indexHTML needs the stub URL; declared here, assigned after srv starts.
	// The handlers close over it by reference, so they see the assigned value.
	var indexHTML string

	mux := http.NewServeMux()
	mux.HandleFunc("/page/1/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(indexHTML))
	})
	mux.HandleFunc("/blackshard/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(postHTML))
	})
	mux.HandleFunc("/upcoming-repacks-10/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(noticeHTML))
	})
	mux.HandleFunc("/page/2/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldBase := fitGirlBase
	fitGirlBase = srv.URL
	defer func() { fitGirlBase = oldBase }()

	// FitGirl index hrefs are absolute; build the fixture the same way.
	indexHTML = `<article><h1 class="entry-title"><a href="` + srv.URL + `/blackshard/" rel="bookmark">BLACKSHARD &#8211; v1.01 + Bonus OST</a></h1></article>
<article><h1 class="entry-title"><a href="` + srv.URL + `/upcoming-repacks-10/" rel="bookmark">Upcoming Repacks</a></h1></article>`

	s := NewFitGirlScraper(srv.Client())

	links, err := s.Browse(context.Background(), models.PlatformPC, 1)
	if err != nil {
		t.Fatalf("Browse page 1: %v", err)
	}
	// One magnet + 2 DDL (datanodes.to + fuckingfast.co; part2 deduped). Notice post skipped.
	if len(links) != 3 {
		t.Fatalf("want 3 links (1 magnet + 2 DDL), got %d: %+v", len(links), links)
	}

	var mag, ddlDN, ddlFF *models.GameLink
	for i := range links {
		switch {
		case links[i].LinkType == models.LinkTypeMagnet:
			mag = &links[i]
		case links[i].LinkType == models.LinkTypeDDL && links[i].Host == "datanodes.to":
			ddlDN = &links[i]
		case links[i].LinkType == models.LinkTypeDDL && links[i].Host == "fuckingfast.co":
			ddlFF = &links[i]
		}
	}
	if mag == nil {
		t.Fatal("missing magnet link")
	}
	if !strings.HasPrefix(mag.URL, "magnet:?xt=urn:btih:E7474AC2DDEA20F15416AC3505D2111721565454&dn=BLACKSHARD") {
		t.Errorf("magnet not entity-decoded: %q", mag.URL)
	}
	if mag.GameSlug != "blackshard" {
		t.Errorf("magnet slug = %q, want blackshard", mag.GameSlug)
	}
	if mag.Title != "BLACKSHARD - v1.01 + Bonus OST" {
		t.Errorf("title = %q, want en-dash normalized to ASCII hyphen", mag.Title)
	}
	if ddlDN == nil || ddlFF == nil {
		t.Fatalf("missing DDL links (datanodes=%v fuckingfast=%v)", ddlDN, ddlFF)
	}
	if !strings.HasSuffix(ddlDN.URL, "part1.rar") {
		t.Errorf("datanodes DDL should be part1 (representative), got %q", ddlDN.URL)
	}
	// Publish date captured from the Yoast meta tag and carried on every link
	// so ingestLinks sets it as the Game's sort key (PC browse order).
	wantPub := int64(1781044201) // 2026-06-09T22:30:01Z
	for _, l := range links {
		if l.SourcePublishedAt != wantPub {
			t.Errorf("link %s: SourcePublishedAt=%d, want %d", l.Host, l.SourcePublishedAt, wantPub)
		}
	}

	// Page 2 is 404 -> Browse returns nil (no error) so a page loop terminates.
	p2, err := s.Browse(context.Background(), models.PlatformPC, 2)
	if err != nil {
		t.Fatalf("Browse page 2 (404): want nil error, got %v", err)
	}
	if p2 != nil {
		t.Errorf("Browse page 2 (404): want nil links, got %d", len(p2))
	}

	// Non-PC platform is a no-op.
	none, err := s.Browse(context.Background(), models.PlatformPS2, 1)
	if err != nil || none != nil {
		t.Errorf("Browse ps2: want nil/nil, got %d / %v", len(none), err)
	}
}

// TestFitGirlSlugFromURL checks slug derivation from per-game post URLs.
func TestFitGirlSlugFromURL(t *testing.T) {
	cases := map[string]string{
		"https://fitgirl-repacks.site/blackshard/":               "blackshard",
		"https://fitgirl-repacks.site/blackshard-repack-updated/": "blackshard-repack-updated",
		"https://fitgirl-repacks.site/upcoming-repacks-10/":      "upcoming-repacks-10",
		"": "",
	}
	for in, want := range cases {
		if got := fitGirlSlugFromURL(in); got != want {
			t.Errorf("fitGirlSlugFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}