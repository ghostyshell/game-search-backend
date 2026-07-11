package scraper

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
	"unicode"

	"game-search-backend/internal/models"
)

// slugify turns a title into a URL-safe slug. ASCII-only, lowercased, non-alpha
// runs collapsed to single hyphens.
func slugify(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	var b strings.Builder
	prevDash := true // suppress leading dash
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.TrimSuffix(b.String(), "-")
	if out == "" {
		out = "untitled"
	}
	return out
}

// linkID returns a stable per-source ID for a link, so re-scrapes upsert the
// same row instead of creating duplicates. SHA1 of source + raw key.
func linkID(source models.Source, key string) string {
	h := sha1.Sum([]byte(string(source) + ":" + key))
	return hex.EncodeToString(h[:])
}

// articlePublishedRe captures the WordPress/Yoast article:published_time meta
// tag from a post page. The category index sorts by this date (newest first), so
// it is the exact sort key the frontend should mirror - scraping wallclock
// (updated_at) instead bubbles games by last re-scrape, not by source order.
// Tolerates intervening attributes (e.g. itemprop="datePublished") between
// property and content, so a non-Yoast SEO plugin emitting a different attribute
// order still matches. Shared by dlpsgame, nswgame, and fitgirl (all WordPress).
var articlePublishedRe = regexp.MustCompile(`<meta\s+property="article:published_time"[^>]*\scontent="([^"]+)"`)

// articlePublishedAt parses the article:published_time meta from a post body
// into Unix seconds. Returns 0 when the tag is absent or unparseable, so the
// caller (ingestLinks) falls back to scrape wallclock for the sort key.
func articlePublishedAt(body []byte) int64 {
	m := articlePublishedRe.FindSubmatch(body)
	if m == nil {
		return 0
	}
	t, err := time.Parse(time.RFC3339, string(m[1]))
	if err != nil {
		return 0
	}
	return t.Unix()
}