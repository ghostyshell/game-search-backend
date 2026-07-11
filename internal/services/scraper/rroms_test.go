package scraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"game-search-backend/internal/models"
)

// TestRRomsParse grounds against the live SvelteKit table structure: rows are
// <tr data-slot="table-row"> with archive.org/download/<id> hrefs. Verifies the
// scraper extracts the item id and (via a stubbed IA metadata server) emits one
// DDL link per original archive file.
func TestRRomsParse(t *testing.T) {
	// Stub IA metadata: one ZIP original + one derivative to confirm the filter.
	iaBody := `{"server":"ia801001.us.archive.org","dir":"/items/sony_playstation_portable_part1","files":[
		{"name":"007 - From Russia with Love (USA).zip","format":"ZIP","source":"original","size":536360260},
		{"name":"sony_playstation_portable_part1_meta.xml","format":"Metadata","source":"metadata"},
		{"name":"sony_playstation_portable_part1_files.xml","format":"Metadata","source":"metadata"}
	]}`
	iaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/metadata/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(iaBody))
	}))
	defer iaSrv.Close()

	// r-roms page with the real table-row + cell structure and one IA link.
	// The IA link is rewritten to point at our stubbed IA server so FetchIAFiles
	// hits the test server. We do that by intercepting via a custom client below.
	pageHTML := `<html><body><table>
		<tr data-slot="table-row">
			<td data-slot="table-cell">PSP Redump .iso (Part 1)</td>
			<td data-slot="table-cell"></td>
			<td data-slot="table-cell"><a href="https://archive.org/download/sony_playstation_portable_part1">Download</a></td>
		</tr>
		<tr data-slot="table-row">
			<td data-slot="table-cell">PSP Redump .iso (Part 2)</td>
			<td data-slot="table-cell"></td>
			<td data-slot="table-cell"><a href="https://archive.org/download/sony_playstation_portable_part2">Download</a></td>
		</tr>
	</table></body></html>`

	// Stub the r-roms page. FetchIAFiles calls archive.org/metadata/... which we
	// can't redirect without overriding the URL, so instead test the parser
	// pieces directly: fetchIAItemIDs over the page, then IAGameLinks over a
	// controlled file list.
	pageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(pageHTML))
	}))
	defer pageSrv.Close()

	s := &RRomsScraper{client: pageSrv.Client()}
	ids, err := s.fetchIAItemIDs(context.Background(), pageSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sony_playstation_portable_part1", "sony_playstation_portable_part2"}
	if len(ids) != 2 {
		t.Fatalf("got %d item ids, want 2: %v", len(ids), ids)
	}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], w)
		}
	}

	// Now exercise the IA metadata fetch against the stubbed IA server by
	// pointing iaMetadataBase at the stub.
	prev := iaMetadataBase
	iaMetadataBase = iaSrv.URL
	defer func() { iaMetadataBase = prev }()
	files, err := FetchIAFiles(context.Background(), iaSrv.Client(), "sony_playstation_portable_part1")
	if err != nil {
		t.Fatalf("FetchIAFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("FetchIAFiles returned %d, want 1 (derivative filtered)", len(files))
	}
	if files[0].Name != "007 - From Russia with Love (USA).zip" {
		t.Errorf("file name = %q", files[0].Name)
	}
	links := IAGameLinks(models.PlatformPSP, models.SourceRRoms, "sony_playstation_portable_part1", files)
	if len(links) != 1 {
		t.Fatalf("IAGameLinks emitted %d, want 1 (derivative filtered)", len(links))
	}
	l := links[0]
	if l.Host != "archive.org" || l.LinkType != models.LinkTypeDDL {
		t.Errorf("link host/type = %q/%q, want archive.org/ddl", l.Host, l.LinkType)
	}
	if l.GameSlug != "007-from-russia-with-love-usa" {
		t.Errorf("slug = %q, want 007-from-russia-with-love-usa", l.GameSlug)
	}
	if l.URL != "https://archive.org/download/sony_playstation_portable_part1/007%20-%20From%20Russia%20with%20Love%20%28USA%29.zip" {
		t.Errorf("url = %q", l.URL)
	}
}

func TestIAItemIDFromURL(t *testing.T) {
	cases := map[string]string{
		"https://archive.org/download/sony_playstation_portable_part1":                     "sony_playstation_portable_part1",
		"https://archive.org/download/sony_playstation_portable_part1/some%20file.zip":      "sony_playstation_portable_part1",
		"https://archive.org/download/PSP_DLC":                                              "PSP_DLC",
		"https://minerva-archive.org/browse/Redump/Sony%20-%20PlayStation%20Portable/":      "",
		"https://archive.org/other/foo":                                                     "",
	}
	for in, want := range cases {
		if got := iaItemIDFromURL(in); got != want {
			t.Errorf("iaItemIDFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}