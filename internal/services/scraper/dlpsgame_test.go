package scraper

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"game-search-backend/internal/models"
)

// TestDLPsGameBrowse grounds the three-level scrape against fixture WordPress
// HTML: the category index lists a PS5 game + nav pages (list-/daily- filtered
// out by the prefix gate even though they end in -ps5); the game post hides its
// links in a base64 data-payload that carries one direct filehost href + one
// sister-site archive URL + a guide nav href; the sister page has the static
// filehost <a href> mirrors. Asserts: base64 decode, sister-hop fetch, one DDL
// per distinct filehost (deduped across direct + sister), nav filtered, and
// that page 2 (404) terminates the loop.
func TestDLPsGameBrowse(t *testing.T) {
	// Sister page: static filehost hrefs + nav (self/gmpg) that must be filtered.
	sisterHTML := `<a href="https://1fichier.com/?abc123">1fichier</a>
<a href="https://akirabox.com/rk9zKRLoZ30l/file">Akirabox</a>
<a href="https://buzzheavier.com/m5cruiga4l6i">Buznew</a>
<a href="https://www.mediafire.com/file/xyz/game.rar">Mediafire</a>
<a href="https://downloadgameps3.net/archives/37646">Rootz</a>
<a href="https://gmpg.org/xfn/11">GMPG</a>`

	// Decoded payload: a direct filehost (datanodes) + the sister archive URL +
	// a guide nav href (downloadgameps3.com). The direct filehost must surface
	// without a sister hop; the sister URL triggers the final-hop fetch; the
	// guide href is filtered as nav.
	mux := http.NewServeMux()
	mux.HandleFunc("/archives/37646", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(sisterHTML))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Redirect the sister hop + base at the stub.
	oldBase := dlpsGameBase
	dlpsGameBase = srv.URL
	defer func() { dlpsGameBase = oldBase }()
	oldSister := dlpsGameSisterHost
	dlpsGameSisterHost = "127.0.0.1"
	defer func() { dlpsGameSisterHost = oldSister }()

	payloadInner := `<a href="https://datanodes.to/xyz/game.rar">Datanodes</a>
<a href="http://127.0.0.1:` + portOf(srv.URL) + `/archives/37646">Rootz</a>
<a href="https://downloadgameps3.com/guide-download-game-use-jdownload/">Tool Download</a>`
	enc := base64.StdEncoding.EncodeToString([]byte(payloadInner))
	postHTML := `<div class="secure-data" data-payload="` + enc + `"></div>`

	mux.HandleFunc("/breathedge-ps5/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(postHTML))
	})

	indexHTML := `<a href="` + srv.URL + `/breathedge-ps5/">Breathedge</a>
<a href="` + srv.URL + `/list-game-ps5/">List Game PS5</a>
<a href="` + srv.URL + `/list-all-game-ps5/">List All PS5</a>
<a href="` + srv.URL + `/daily-update-on-changes-to-game/">Daily Update</a>`
	mux.HandleFunc("/category/ps5/page/1/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(indexHTML))
	})
	mux.HandleFunc("/category/ps5/page/2/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	s := NewDLPsGameScraper(srv.Client())

	links, err := s.Browse(context.Background(), models.PlatformPS5, 1)
	if err != nil {
		t.Fatalf("Browse page 1: %v", err)
	}
	// Expect 6 links: 5 DDL (1 direct datanodes + 4 from sister: 1fichier,
	// akirabox, buzzheavier, mediafire) + 1 source page (the dlpsgame.com post
	// URL). Sister self-link, gmpg, and guide nav filtered.
	if len(links) != 6 {
		t.Fatalf("expected 6 links (5 ddl + 1 source), got %d: %+v", len(links), linkHosts(links))
	}
	hosts := map[string]bool{}
	var sourceLinks []models.GameLink
	for _, l := range links {
		if l.GameSlug != "breathedge-ps5" {
			t.Errorf("link %s: slug=%s want breathedge-ps5", l.Host, l.GameSlug)
		}
		if l.Platform != models.PlatformPS5 {
			t.Errorf("link %s: platform=%s want ps5", l.Host, l.Platform)
		}
		if l.Source != models.SourceDLPsGame {
			t.Errorf("link %s: source=%s want dlpsgame", l.Host, l.Source)
		}
		if l.LinkType == models.LinkTypeSource {
			sourceLinks = append(sourceLinks, l)
			continue
		}
		if l.LinkType != models.LinkTypeDDL {
			t.Errorf("link %s: expected ddl, got %s", l.Host, l.LinkType)
		}
		hosts[l.Host] = true
	}
	for _, want := range []string{"datanodes.to", "1fichier.com", "akirabox.com", "buzzheavier.com", "www.mediafire.com"} {
		if !hosts[want] {
			t.Errorf("missing filehost %s; got %v", want, hosts)
		}
	}
	for _, bad := range []string{"downloadgameps3.net", "downloadgameps3.com", "gmpg.org", "127.0.0.1"} {
		if hosts[bad] {
			t.Errorf("nav/sister host %s should have been filtered", bad)
		}
	}
	// Source page link: one, pointing at the post URL, host dlpsgame.com, with a
	// distinct _id so it never collides with the magnet/DDL link IDs.
	if len(sourceLinks) != 1 {
		t.Fatalf("expected 1 source link, got %d: %+v", len(sourceLinks), sourceLinks)
	}
	src := sourceLinks[0]
	wantURL := srv.URL + "/breathedge-ps5/"
	if src.URL != wantURL {
		t.Errorf("source link URL=%s want %s", src.URL, wantURL)
	}
	if src.Host != "dlpsgame.com" {
		t.Errorf("source link host=%s want dlpsgame.com", src.Host)
	}
	if src.ID != linkID(models.SourceDLPsGame, "source:breathedge-ps5") {
		t.Errorf("source link id=%s want %s", src.ID, linkID(models.SourceDLPsGame, "source:breathedge-ps5"))
	}

	// Page 2 (404) terminates the loop cleanly.
	links2, err := s.Browse(context.Background(), models.PlatformPS5, 2)
	if err != nil {
		t.Fatalf("Browse page 2: %v", err)
	}
	if len(links2) != 0 {
		t.Errorf("page 2 should be empty (404), got %d links", len(links2))
	}

	// A 5xx on the index is a transient outage, not end-of-pages: it must
	// surface as an error so DLPsGameSync doesn't silently skip the platform.
	mux.HandleFunc("/category/ps5/page/3/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if _, err := s.Browse(context.Background(), models.PlatformPS5, 3); err == nil {
		t.Error("page 3 (503) should return an error, got nil")
	}
}

// TestDLPsGameNavSlug verifies the prefix gate drops admin/index slugs that
// also end in the platform suffix (list-all-game-ps2, list-game-ps5, ...).
func TestDLPsGameNavSlug(t *testing.T) {
	cases := map[string]bool{
		"breathedge-ps5":                  false, // game
		"jazzpunk-directors-cut-ps4-pkg":  false, // game
		"list-all-game-ps2":               true,  // index
		"list-game-ps5":                   true,  // index
		"daily-update-on-changes-to-game": true,  // admin
		"guide-download-game":             true,  // guide
	}
	for slug, wantNav := range cases {
		if got := dlpsGameIsNavSlug(slug); got != wantNav {
			t.Errorf("dlpsGameIsNavSlug(%q)=%v want %v", slug, got, wantNav)
		}
	}
}

// TestDLPsGameSlugToken verifies the platform token filter accepts format-
// suffixed slugs (-ps4-pkg, -ps3-iso) and rejects nav slugs that also contain
// the token. Guards the HasSuffix -> Contains(token) fix.
func TestDLPsGameSlugToken(t *testing.T) {
	cat := "ps4"
	token := "-" + cat + "-"
	accept := []string{"jazzpunk-directors-cut-ps4-pkg", "cat-from-hell-2-ps4-fpkg", "narcosis-ps4"}
	reject := []string{"list-all-game-ps4", "list-game-ps4", "daily-update-on-changes-to-game"}
	for _, s := range accept {
		if !strings.Contains("-"+s+"-", token) {
			t.Errorf("accept %s: token %s not found (should accept)", s, token)
		}
	}
	for _, s := range reject {
		if !dlpsGameIsNavSlug(s) {
			t.Errorf("reject %s: not caught by nav prefix gate", s)
		}
	}
}

func linkHosts(links []models.GameLink) []string {
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, l.Host)
	}
	return out
}

// portOf extracts the port from an httptest URL like http://127.0.0.1:38001.
func portOf(u string) string {
	i := strings.LastIndex(u, ":")
	if i < 0 {
		return ""
	}
	return u[i+1:]
}