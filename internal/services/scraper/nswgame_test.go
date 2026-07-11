package scraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"game-search-backend/internal/models"
)

// TestNSWGameBrowse grounds the two-level scrape against fixture WordPress
// HTML: the category index lists a game + a nav page (filtered out); the game
// page has DDL mirrors on multiple filehosts behind plain <a> tags, plus nav
// links (filtered out by the blocklist). Asserts one DDL per distinct filehost,
// slug derivation, and that page 2 (404) terminates the loop.
func TestNSWGameBrowse(t *testing.T) {
	var indexHTML string
	postHTML := `<h1>Axel Quest</h1>
<meta property="article:published_time" content="2026-07-06T15:12:59+00:00" />
<a href="https://megaup.net/fbe47c1dec176fa7beaf5e5108bc9eee/AXLQST.rar">Megaup</a>
<a href="https://1fichier.com/?hjburnxwow18jx5d2jyk&amp;af=3743721">1fichier</a>
<a href="https://send.now/eyv2i501kgtx">Send</a>
<a href="https://datanodes.to/6ukh702577ym/AXLQST.rar">Datanodes</a>
<a href="https://downloadgameps3.com/guide-download-game-use-jdownload/">Tool Download</a>
<a href="https://nswgame.com/list-all-game-switch/">List All</a>
<a href="https://www.youtube.com/channel/x">YouTube</a>`

	mux := http.NewServeMux()
	mux.HandleFunc("/category/switch/page/1/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(indexHTML))
	})
	mux.HandleFunc("/category/switch/page/2/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldBase := nswGameBase
	nswGameBase = srv.URL
	defer func() { nswGameBase = oldBase }()

	// Index lists a game (slug contains "nintendo-switch") + a nav page (does not).
	indexHTML = `<a href="` + srv.URL + `/axel-quest-nintendo-switch-nsp-xci-nsz-download-free/">Axel Quest</a>
<a href="` + srv.URL + `/daily-update-on-changes-to-game-switch/">Daily Update</a>`

	// Re-register the game-page handler using the stub URL (set after srv starts).
	mux.HandleFunc("/axel-quest-nintendo-switch-nsp-xci-nsz-download-free/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(postHTML))
	})

	s := NewNSWGameScraper(srv.Client())

	links, err := s.Browse(context.Background(), models.PlatformSwitch, 1)
	if err != nil {
		t.Fatalf("Browse page 1: %v", err)
	}
	// 4 filehost mirrors (megaup, 1fichier, send, datanodes). Tool/youtube/list-all filtered.
	if len(links) != 4 {
		t.Fatalf("want 4 DDL links (one per filehost), got %d: %+v", len(links), links)
	}
	wantHosts := map[string]bool{"megaup.net": false, "1fichier.com": false, "send.now": false, "datanodes.to": false}
	for _, l := range links {
		if l.GameSlug != "axel-quest-nintendo-switch-nsp-xci-nsz-download-free" {
			t.Errorf("slug = %q", l.GameSlug)
		}
		if l.LinkType != models.LinkTypeDDL {
			t.Errorf("link type = %v, want ddl", l.LinkType)
		}
		if _, ok := wantHosts[l.Host]; ok {
			wantHosts[l.Host] = true
		} else {
			t.Errorf("unexpected host %q", l.Host)
		}
	}
	for h, seen := range wantHosts {
		if !seen {
			t.Errorf("missing mirror on host %q", h)
		}
	}
	if links[0].Title != "Axel Quest" {
		t.Errorf("title = %q, want Axel Quest", links[0].Title)
	}
	if !strings.HasPrefix(links[0].URL, "https://") {
		t.Errorf("URL should be https: %q", links[0].URL)
	}
	// Publish date captured from the Yoast meta tag and carried on every link
	// so ingestLinks sets it as the Game's sort key (Switch browse order).
	wantPub := int64(1783350779) // 2026-07-06T15:12:59Z
	for _, l := range links {
		if l.SourcePublishedAt != wantPub {
			t.Errorf("link %s: SourcePublishedAt=%d, want %d", l.Host, l.SourcePublishedAt, wantPub)
		}
	}

	// Page 2 (404) terminates the loop with no error.
	p2, err := s.Browse(context.Background(), models.PlatformSwitch, 2)
	if err != nil {
		t.Fatalf("Browse page 2 (404): want nil error, got %v", err)
	}
	if p2 != nil {
		t.Errorf("Browse page 2 (404): want nil links, got %d", len(p2))
	}

	// Non-Switch platform is a no-op.
	none, err := s.Browse(context.Background(), models.PlatformPC, 1)
	if err != nil || none != nil {
		t.Errorf("Browse pc: want nil/nil, got %d / %v", len(none), err)
	}
}

// TestNSWGameSlugFromURL checks slug derivation.
func TestNSWGameSlugFromURL(t *testing.T) {
	cases := map[string]string{
		"https://nswgame.com/axel-quest-nintendo-switch-nsp-xci-nsz-download-free/": "axel-quest-nintendo-switch-nsp-xci-nsz-download-free",
		"https://nswgame.com/death-run-nintendo-switch-nsp-xci-nsz-download-free/": "death-run-nintendo-switch-nsp-xci-nsz-download-free",
		"": "",
	}
	for in, want := range cases {
		if got := nswGameSlugFromURL(in); got != want {
			t.Errorf("nswGameSlugFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}