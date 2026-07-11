package scraper

import "testing"

// TestArticlePublishedAt grounds the shared publish-date capture against the
// live WordPress/Yoast meta tag used by dlpsgame, nswgame, and fitgirl. The
// category index sorts by this date, so it is the sort key the frontend must
// mirror. Verifies parse from the meta tag, 0 when absent, 0 on an unparseable
// date, and that an intervening attribute (itemprop) between property and
// content still matches under the permissive regex.
func TestArticlePublishedAt(t *testing.T) {
	body := []byte(`<html><head>
<meta property="article:published_time" content="2026-07-06T02:39:16+00:00" />
</head><body>x</body></html>`)
	got := articlePublishedAt(body)
	want := int64(1783305556) // 2026-07-06T02:39:16Z
	if got != want {
		t.Errorf("articlePublishedAt with meta tag: got %d, want %d", got, want)
	}

	// Absent tag -> 0 (caller falls back to scrape wallclock).
	if got := articlePublishedAt([]byte(`<html><body>no meta here</body></html>`)); got != 0 {
		t.Errorf("articlePublishedAt with no tag: got %d, want 0", got)
	}
	// Unparseable content -> 0, not a panic.
	if got := articlePublishedAt([]byte(`<meta property="article:published_time" content="not-a-date" />`)); got != 0 {
		t.Errorf("articlePublishedAt with bad date: got %d, want 0", got)
	}
	// Intervening attribute between property and content (a non-Yoast SEO plugin
	// emitting itemprop first) still matches under the permissive regex.
	if got := articlePublishedAt([]byte(`<meta property="article:published_time" itemprop="datePublished" content="2026-07-06T02:39:16+00:00" />`)); got != want {
		t.Errorf("articlePublishedAt with intervening attr: got %d, want %d", got, want)
	}
}