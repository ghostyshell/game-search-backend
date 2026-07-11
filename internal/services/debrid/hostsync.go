// Package debrid fetches the public host lists from TorBox + Real-Debrid and
// builds the canonical host -> providers map used to tag game links.
package debrid

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"game-search-backend/internal/models"
)

// HostSyncer fetches the live TorBox + Real-Debrid host lists and merges them
// into a single host -> providers map.
type HostSyncer struct {
	client *http.Client
}

// NewHostSyncer builds a HostSyncer with a short-timeout client. No API keys:
// both /hosts endpoints are public.
func NewHostSyncer() *HostSyncer {
	return &HostSyncer{client: &http.Client{Timeout: 20 * time.Second}}
}

const (
	torboxHostersURL = "https://api.torbox.app/v1/api/webdl/hosters"
	rdDomainsURL     = "https://api.real-debrid.com/rest/1.0/hosts/domains"
)

// FetchHosts returns the merged host -> providers map. Each host domain maps to
// the providers that support it.
func (h *HostSyncer) FetchHosts(ctx context.Context) (map[string][]models.DebridProvider, error) {
	out := make(map[string][]models.DebridProvider)

	tb, tbErr := h.fetchTorboxHosters(ctx)
	if tbErr == nil {
		for _, host := range tb {
			host = NormHost(host)
			if host == "" {
				continue
			}
			out[host] = appendUnique(out[host], models.ProviderTorBox)
		}
	} // ponytail: one provider failing to answer must not block the other.

	rd, rdErr := h.fetchRDDomains(ctx)
	if rdErr == nil {
		for _, host := range rd {
			host = NormHost(host)
			if host == "" {
				continue
			}
			out[host] = appendUnique(out[host], models.ProviderRealDebrid)
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("debrid host sync: both providers returned no hosts (torbox err=%v, rd err=%v)", tbErr, rdErr)
	}
	return out, nil
}

// fetchTorboxHosters returns TorBox's supported web-download host domains.
// TorBox /v1/api/webdl/hosters returns {"data":[{"domains":["rapidgator.net",...], ...}, ...]}.
func (h *HostSyncer) fetchTorboxHosters(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, torboxHostersURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("torbox hosters HTTP %d", resp.StatusCode)
	}
	var wrapped struct {
		Success bool `json:"success"`
		Data    []struct {
			Domains []string `json:"domains"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapped); err != nil {
		return nil, err
	}
	var hosts []string
	for _, h := range wrapped.Data {
		hosts = append(hosts, h.Domains...)
	}
	return hosts, nil
}

// fetchRDDomains returns Real-Debrid's supported host domains. RD
// /rest/1.0/hosts/domains returns a bare JSON array of strings (public, no auth).
func (h *HostSyncer) fetchRDDomains(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rdDomainsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rd domains HTTP %d", resp.StatusCode)
	}
	var hosts []string
	if err := json.NewDecoder(resp.Body).Decode(&hosts); err != nil {
		return nil, err
	}
	return hosts, nil
}

// NormHost lowercases and strips a leading "www." / scheme. Exported so the
// ingest runner can normalize link hosts against the cached map.
func NormHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(h, "http://")
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "www.")
	h = strings.TrimSuffix(h, "/")
	return h
}

func appendUnique(s []models.DebridProvider, p models.DebridProvider) []models.DebridProvider {
	for _, existing := range s {
		if existing == p {
			return s
		}
	}
	return append(s, p)
}