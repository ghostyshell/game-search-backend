// Package metadata enriches games from RAWG (+ optional TheGamesDB). The RAWG
// client rotates a pool of API keys around the per-key request limit; when a
// key returns a rate-limit response it is marked exhausted and the next key is
// used. When all keys are exhausted, ErrAllKeysExhausted is returned so the
// ingest can stop cleanly and the operator can append a fresh key + re-run.
package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrAllKeysExhausted signals every key in the pool has hit RAWG's limit. The
// caller should surface this so the operator can supply a new key and re-run.
var ErrAllKeysExhausted = fmt.Errorf("all RAWG API keys exhausted")

// RawgClient talks to RAWG with a rotating key pool.
type RawgClient struct {
	base       string
	keys       []string
	httpClient *http.Client
	mu         sync.Mutex
	current    int
	exhausted  map[int]bool
}

// NewRawgClient builds a RAWG client. keys may be empty (all calls then return
// ErrAllKeysExhausted immediately).
func NewRawgClient(base string, keys []string) *RawgClient {
	if base == "" {
		base = "https://api.rawg.io/api"
	}
	return &RawgClient{
		base:       base,
		keys:       keys,
		httpClient: &http.Client{Timeout: 20 * time.Second},
		exhausted:  make(map[int]bool),
	}
}

// HasKeys reports whether any key is configured.
func (c *RawgClient) HasKeys() bool { return len(c.keys) > 0 }

// RawgGame is the enrichment payload lifted from RAWG's /games response.
type RawgGame struct {
	ID               int     `json:"id"`
	Name             string  `json:"name"`
	Rating           float64 `json:"rating"`
	Released         string  `json:"released"`
	BackgroundImage  string  `json:"background_image"`
	Genres           []struct {
		Name string `json:"name"`
	} `json:"genres"`
	Platforms []struct {
		Platform struct {
			Slug string `json:"slug"`
		} `json:"platform"`
	} `json:"platforms"`
	// short_screenshots comes free on the search list response; capturing it
	// here avoids a separate /games/{id}/screenshots call for the gallery.
	ShortScreenshots []struct {
		Image string `json:"image"`
	} `json:"short_screenshots"`
}

// RawgGameDetail is the full metadata payload from RAWG's /games/{id} detail
// endpoint. The list response omits description/developers/publishers/
// metacritic_url; this is the authoritative source for "store all RAWG metadata".
type RawgGameDetail struct {
	ID                      int     `json:"id"`
	Name                    string  `json:"name"`
	Rating                  float64 `json:"rating"`
	Released                string  `json:"released"`
	BackgroundImage         string  `json:"background_image"`
	BackgroundImageAdditional string `json:"background_image_additional"`
	Description              string  `json:"description"`       // HTML
	DescriptionRaw           string  `json:"description_raw"`  // plaintext (preferred)
	Metacritic               *int    `json:"metacritic"`       // pointer: 0 != absent
	MetacriticURL            string  `json:"metacritic_url"`
	ESRBRating               *struct {
		Name string `json:"name"`
	} `json:"esrb_rating"`
	Developers []struct {
		Name string `json:"name"`
	} `json:"developers"`
	Publishers []struct {
		Name string `json:"name"`
	} `json:"publishers"`
	Tags []struct {
		Name string `json:"name"`
	} `json:"tags"`
	Stores []struct {
		URL   string `json:"url"`
		Store struct {
			Name string `json:"name"`
		} `json:"store"`
	} `json:"stores"`
	Genres []struct {
		Name string `json:"name"`
	} `json:"genres"`
	Playtime   int    `json:"playtime"`
	RedditURL  string `json:"reddit_url"`
}

// SearchByTitle returns games matching a title (RAWG search is keyword fuzzy).
// Platform filtering is done client-side against the platform slugs RAWG
// returns in each result, so we never depend on RAWG's numeric platform IDs.
func (c *RawgClient) SearchByTitle(ctx context.Context, title string) ([]RawgGame, error) {
	q := url.Values{}
	q.Set("search", title)
	q.Set("page_size", "20")
	var body struct {
		Results []RawgGame `json:"results"`
	}
	if err := c.get(ctx, "/games", q, &body); err != nil {
		return nil, err
	}
	return body.Results, nil
}

// GameDetail fetches the full metadata for one RAWG game by id. The list
// response omits description/developers/publishers/metacritic_url, so this is
// the source for the full enrichment. Reuses c.get for key rotation + rate-
// limit handling, so detail calls drain the same key pool as search calls.
func (c *RawgClient) GameDetail(ctx context.Context, id int) (*RawgGameDetail, error) {
	var detail RawgGameDetail
	if err := c.get(ctx, fmt.Sprintf("/games/%d", id), url.Values{}, &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}

// get executes one GET against RAWG, retrying with the next key on a rate-limit
// response until the pool is exhausted. A single 429 first retries the same
// key after a short backoff (a transient per-second throttle should clear); a
// second 429 from the same key is treated as the per-key monthly limit and the
// key is marked exhausted so the pool rotates.
func (c *RawgClient) get(ctx context.Context, path string, q url.Values, out any) error {
	if len(c.keys) == 0 {
		return ErrAllKeysExhausted
	}
	for attempt := 0; attempt < len(c.keys); attempt++ {
		idx := c.pickKey()
		if idx < 0 {
			return ErrAllKeysExhausted
		}
		q.Set("key", c.keys[idx])
		if err := c.doWithRetry(ctx, path, q, idx, out); err == nil {
			return nil
		} else if err == errRateLimited {
			// retry handled inside doWithRetry; if it returned errRateLimited
			// the key is now marked exhausted - rotate to the next key.
			continue
		} else {
			return err
		}
	}
	return ErrAllKeysExhausted
}

// errRateLimited signals the current key hit the per-key limit and is now
// marked exhausted; the caller should rotate.
var errRateLimited = fmt.Errorf("rawg key rate-limited")

func (c *RawgClient) doWithRetry(ctx context.Context, path string, q url.Values, idx int, out any) error {
	for retry := 0; retry < 2; retry++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return err
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		// RAWG signals the per-key monthly limit as HTTP 401 with body
		// {"error":"The monthly API limit reached"} (not 429, and the wording is
		// "monthly API limit reached", not "Request limit reached"). Gate on the
		// body string, not bare 401, so a genuinely invalid key (401 with a
		// different body) still surfaces as a plain error instead of being
		// silently marked exhausted.
		rateLimited := resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusPaymentRequired ||
			strings.Contains(string(respBody), "Request limit reached") ||
			strings.Contains(string(respBody), "monthly API limit reached")
		if rateLimited {
			if retry == 0 {
				// ponytail: one backoff retry clears a transient per-second
				// throttle without burning a key on a single hiccup.
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(1 * time.Second):
				}
				continue
			}
			c.markExhausted(idx)
			return errRateLimited
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("rawg %s: HTTP %d: %s", path, resp.StatusCode, truncate(respBody, 200))
		}
		return json.Unmarshal(respBody, out)
	}
	return errRateLimited
}

// pickKey returns the index of the current non-exhausted key, or -1 if all are
// exhausted. Caller holds no lock (method locks).
func (c *RawgClient) pickKey() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := 0; i < len(c.keys); i++ {
		idx := (c.current + i) % len(c.keys)
		if !c.exhausted[idx] {
			c.current = idx
			return idx
		}
	}
	return -1
}

func (c *RawgClient) markExhausted(idx int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.exhausted[idx] = true
	remaining := len(c.keys) - len(c.exhausted)
	fmt.Printf("rawg: key #%d exhausted (%d keys remaining)\n", idx+1, remaining)
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// platformToRawgSlug maps our normalized Platform to the RAWG platform slug
// RAWG's /games?platforms= filter accepts.
var platformToRawgSlug = map[string]string{
	"xbox360": "xbox360",
	"pc":      "pc",
	"ps3":     "playstation3",
	"ps4":     "playstation4",
	"ps5":     "playstation5",
	"ps2":     "playstation2",
	"psp":     "psp",
	"psvita":  "ps-vita",
	"switch":  "nintendo-switch",
}

// PlatformToRawgSlug is exported for tests.
func PlatformToRawgSlug(p string) string { return platformToRawgSlug[p] }