package jobs

import (
	"testing"

	"game-search-backend/internal/models"
)

// TestTagLinks is the one self-check for the debrid-eligibility tagger: torrent
// links always get both providers; DDL links get the host's providers (or none
// if the host is unknown).
func TestTagLinks(t *testing.T) {
	r := &Runner{hosts: map[string][]models.DebridProvider{
		"mega.nz": {models.ProviderTorBox},
	}}
	links := []models.GameLink{
		{LinkType: models.LinkTypeTorrent, Host: "minerva-archive.org"},
		{LinkType: models.LinkTypeMagnet, Host: "anything"},
		{LinkType: models.LinkTypeDDL, Host: "mega.nz"},
		{LinkType: models.LinkTypeDDL, Host: "unknown.host"},
	}
	out := r.tagLinks(links)
	if len(out[0].SupportedBy) != 2 || out[0].SupportedBy[0] != models.ProviderTorBox {
		t.Errorf("torrent link should be both providers, got %v", out[0].SupportedBy)
	}
	if len(out[1].SupportedBy) != 2 {
		t.Errorf("magnet link should be both providers, got %v", out[1].SupportedBy)
	}
	if len(out[2].SupportedBy) != 1 || out[2].SupportedBy[0] != models.ProviderTorBox {
		t.Errorf("mega.nz ddl should be torbox-only, got %v", out[2].SupportedBy)
	}
	if len(out[3].SupportedBy) != 0 {
		t.Errorf("unknown host ddl should have no providers, got %v", out[3].SupportedBy)
	}
}