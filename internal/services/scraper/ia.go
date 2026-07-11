package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"game-search-backend/internal/models"
)

// IAFile is one enumerable file in an archive.org item. Size is intentionally
// omitted: IA returns it as a string for some files and we don't use it.
type IAFile struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Source string `json:"source"`
}

// iaMetadata is the subset of archive.org/metadata/<id> we use.
type iaMetadata struct {
	Server string   `json:"server"`
	Dir    string   `json:"dir"`
	Files  []IAFile `json:"files"`
}

// IADDLURL returns the canonical download URL for a file in an archive.org item.
// archive.org/download/<id>/<name> redirects to the right server; we do not need
// to pin <server>/<dir>/<name> ourselves.
func IADDLURL(itemID, fileName string) string {
	return "https://archive.org/download/" + url.PathEscape(itemID) + "/" + url.PathEscape(fileName)
}

// iaArchiveExt are the file formats we treat as downloadable game payloads.
// Matches the IA "format" field (case-insensitive) and the file extension.
var iaArchiveExt = map[string]bool{
	".zip": true, ".7z": true, ".rar": true, ".iso": true, ".chd": true,
	".nsp": true, ".xci": true, ".rvz": true, ".wbfs": true, ".wad": true,
	".elf": true, ".dmg": true,
}

// iaMetadataBase is the archive.org base. Overridable for tests.
var iaMetadataBase = "https://archive.org"

// FetchIAFiles enumerates the "original" (non-derivative) archive files in an
// archive.org item. Filters out IA's metadata/xml/sqlite derivatives and
// non-archive files. Used by r-roms (and the IA Xbox source) to turn a
// collection link into per-file DDL GameLinks.
func FetchIAFiles(ctx context.Context, client *http.Client, itemID string) ([]IAFile, error) {
	return fetchIAFilesFrom(ctx, client, iaMetadataBase, itemID)
}

// fetchIAFilesFrom is FetchIAFiles with an injectable base URL (for tests).
func fetchIAFilesFrom(ctx context.Context, client *http.Client, base, itemID string) ([]IAFile, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	apiURL := base + "/metadata/" + url.PathEscape(itemID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ia metadata %s: HTTP %d", itemID, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var meta iaMetadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("ia metadata %s: parse: %w", itemID, err)
	}
	var out []IAFile
	for _, f := range meta.Files {
		if f.Name == "" {
			continue
		}
		// ponytail: source=="original" is IA's marker for the user-uploaded
		// payload; derivatives (_meta.xml, _files.xml, .sqlite, .jpg thumbs)
		// have other sources. Also gate on ext so we skip stray non-ROM files.
		if strings.ToLower(f.Source) != "original" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f.Name))
		if !iaArchiveExt[ext] {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// IAGameLinks turns one IA item's original files into DDL GameLinks for a
// platform+source. Title is the filename minus extension; slug is slugify(title).
func IAGameLinks(platform models.Platform, source models.Source, itemID string, files []IAFile) []models.GameLink {
	now := time.Now().Unix()
	seen := make(map[string]struct{}, len(files))
	var links []models.GameLink
	for _, f := range files {
		title := strings.TrimSuffix(f.Name, filepath.Ext(f.Name))
		slug := slugify(title)
		if slug == "" {
			continue
		}
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		links = append(links, models.GameLink{
			ID:          linkID(source, itemID+":"+f.Name),
			GameSlug:    slug,
			Platform:    platform,
			Source:      source,
			Title:       title,
			Host:        "archive.org",
			URL:         IADDLURL(itemID, f.Name),
			LinkType:    models.LinkTypeDDL,
			SupportedBy: nil, // host sync fills this (archive.org eligibility TBD)
			UpdatedAt:   now,
			CreatedAt:   now,
		})
	}
	return links
}