package scraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"game-search-backend/internal/models"
)

// TestMinervaParse is the one self-check for the Minerva parser. Feeds a small
// browse fixture and asserts per-file DDL links are parsed with the right
// slug/host/url and that duplicate slugs collapse.
func TestMinervaParse(t *testing.T) {
	fixture := `<html><body>
	<a href="/rom?name=Redump/Microsoft%20-%20Xbox%20360/Halo%203.zip">Halo 3.zip</a>
	<a href="/rom?name=Redump/Microsoft%20-%20Xbox%20360/Gears%20of%20War.7z">Gears.7z</a>
	<a href="/other?name=ignored">ignored</a>
	<a href="/rom?name=Redump/Microsoft%20-%20Xbox%20360/Dupe.zip">Dupe.zip</a>
	<a href="/rom?name=Redump/Microsoft%20-%20Xbox%20360/Dupe.zip">Dupe.zip</a>
	</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fixture))
	}))
	defer srv.Close()

	// Swap the browse URL map to point at the test server; drop collection
	// torrents so assertions count only parsed files.
	orig := minervaBrowseURLs[models.PlatformXbox360]
	minervaBrowseURLs[models.PlatformXbox360] = []string{srv.URL + "/browse"}
	defer func() { minervaBrowseURLs[models.PlatformXbox360] = orig }()
	origT := minervaCollectionTorrents[models.PlatformXbox360]
	minervaCollectionTorrents[models.PlatformXbox360] = nil
	defer func() { minervaCollectionTorrents[models.PlatformXbox360] = origT }()

	m := NewMinervaScraper(srv.Client())
	links, err := m.Browse(context.Background(), models.PlatformXbox360, 1)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(links) != 3 {
		t.Fatalf("want 3 links (Halo, Gears, Dupe dedup), got %d: %+v", len(links), links)
	}
	wantSlug := "halo-3"
	if links[0].GameSlug != wantSlug || links[0].Host != "minerva-archive.org" || links[0].LinkType != models.LinkTypeDDL {
		t.Errorf("link[0] = slug=%s host=%s type=%s, want %s/minerva-archive.org/ddl",
			links[0].GameSlug, links[0].Host, links[0].LinkType, wantSlug)
	}
	if !strings.HasSuffix(links[0].URL, "/rom?name=Redump/Microsoft%20-%20Xbox%20360/Halo%203.zip") {
		t.Errorf("unexpected url: %s", links[0].URL)
	}
	// Collection torrents (dropped here) would be torrent-typed; the tagger test
	// lives in the jobs package where tagLinks is defined.
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Halo 3":         "halo-3",
		"Gears of War 2": "gears-of-war-2",
		"  ---!!Foo!!---": "foo",
		"":               "untitled",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}