// Package models holds the domain types for game-search-backend.
package models

// Platform is a normalized game platform identifier.
type Platform string

const (
	PlatformXbox360 Platform = "xbox360"
	PlatformPC      Platform = "pc"
	PlatformPS3     Platform = "ps3"
	PlatformPS4     Platform = "ps4"
	PlatformPS5     Platform = "ps5"
	PlatformPS2     Platform = "ps2"
	PlatformPSP     Platform = "psp"
	PlatformPSVita  Platform = "psvita"
	PlatformSwitch  Platform = "switch"
)

// Source is a normalized download-source identifier.
type Source string

const (
	SourceMinerva  Source = "minerva"
	SourceIA       Source = "ia"
	SourceFitGirl  Source = "fitgirl"
	SourceDLPsGame Source = "dlpsgame"
	SourceRRoms    Source = "rroms"
	SourceNSWGame  Source = "nswgame"
)

// LinkType describes how a link is consumed.
type LinkType string

const (
	LinkTypeMagnet LinkType = "magnet"
	LinkTypeTorrent LinkType = "torrent" // a .torrent file URL
	LinkTypeDDL    LinkType = "ddl"      // a direct file-host URL
	LinkTypeSource LinkType = "source"  // the source site's post/page URL (not a download)
)

// DebridProvider is a supported debrid provider.
type DebridProvider string

const (
	ProviderTorBox     DebridProvider = "torbox"
	ProviderRealDebrid  DebridProvider = "realdebrid"
)

// Game is a catalog entry. One game may have many GameLinks across sources/hosts.
type Game struct {
	Slug     string   `json:"slug" bson:"slug"`
	Title    string   `json:"title" bson:"title"`
	Platform Platform `json:"platform" bson:"platform"`
	Source   Source   `json:"source" bson:"source"`
	// Links are resolved lazily; the games list embeds link counts for cheap listing.
	LinkCount int `json:"linkCount" bson:"link_count"`

	// Enrichment (RAWG + TheGamesDB). $setOnInsert so re-ingest never clobbers.
	RawgID        int64  `json:"rawgId,omitempty" bson:"rawg_id,omitempty"`
	RawgRating    float64 `json:"rawgRating,omitempty" bson:"rawg_rating,omitempty"`
	RawgCover     string `json:"rawgCover,omitempty" bson:"rawg_cover,omitempty"`
	RawgReleased  string `json:"rawgReleased,omitempty" bson:"rawg_released,omitempty"`
	RawgGenres    []string `json:"rawgGenres,omitempty" bson:"rawg_genres,omitempty"`

	// Full RAWG detail (fetched from /games/{id} after the search match). The
	// list endpoint omits these; rawg_detail_fetched gates the backfill so the
	// existing ragenrich run re-enriches entries that pre-date this field.
	RawgDescription  string      `json:"rawgDescription,omitempty" bson:"rawg_description,omitempty"`
	RawgMetacritic   *int        `json:"rawgMetacritic,omitempty" bson:"rawg_metacritic,omitempty"`
	RawgMetacriticURL string     `json:"rawgMetacriticUrl,omitempty" bson:"rawg_metacritic_url,omitempty"`
	RawgESRB        string      `json:"rawgEsrb,omitempty" bson:"rawg_esrb,omitempty"`
	RawgDevelopers  []string    `json:"rawgDevelopers,omitempty" bson:"rawg_developers,omitempty"`
	RawgPublishers  []string    `json:"rawgPublishers,omitempty" bson:"rawg_publishers,omitempty"`
	RawgTags        []string    `json:"rawgTags,omitempty" bson:"rawg_tags,omitempty"`
	RawgStores      []RawgStore `json:"rawgStores,omitempty" bson:"rawg_stores,omitempty"`
	RawgScreenshots []string    `json:"rawgScreenshots,omitempty" bson:"rawg_screenshots,omitempty"`
	RawgPlaytime    int         `json:"rawgPlaytime,omitempty" bson:"rawg_playtime,omitempty"`
	RawgRedditURL   string      `json:"rawgRedditUrl,omitempty" bson:"rawg_reddit_url,omitempty"`
	RawgDetailFetched bool       `json:"rawgDetailFetched" bson:"rawg_detail_fetched"`

	TGDBID        int    `json:"tgdbId,omitempty" bson:"tgdb_id,omitempty"`
	TGDBCover     string `json:"tgdbCover,omitempty" bson:"tgdb_cover,omitempty"`
	EnrichedAt    int64  `json:"enrichedAt,omitempty" bson:"enriched_at,omitempty"`
	EnrichedRawg  bool   `json:"enrichedRawg" bson:"enriched_rawg"`
	EnrichedTGDB  bool   `json:"enrichedTgdb" bson:"enriched_tgdb"`

	// SourcePublishedAt is the source post's publish date (Unix seconds) when the
	// scraper can capture it (dlpsgame via article:published_time). It is the browse
	// sort key so the frontend matches the source's own newest-first order instead
	// of scraping wallclock (updated_at). Falls back to updated_at-equivalent (now)
	// for sources without a publish date; see ingestLinks. Zero only before the
	// one-time Migrate backfill copies updated_at into it.
	SourcePublishedAt int64 `json:"sourcePublishedAt,omitempty" bson:"source_published_at,omitempty"`

	UpdatedAt int64 `json:"updatedAt" bson:"updated_at"`
	CreatedAt int64 `json:"createdAt" bson:"created_at"`
}

// GameLink is a single downloadable link for a game, tagged with its host and
// which debrid providers can handle it.
type GameLink struct {
	ID          string        `json:"id" bson:"_id"`
	GameSlug    string        `json:"gameSlug" bson:"game_slug"`
	Platform    Platform      `json:"platform" bson:"platform"`
	Source      Source        `json:"source" bson:"source"`
	Title       string        `json:"title" bson:"title"`
	Host        string        `json:"host" bson:"host"`         // e.g. archive.org, mega.nz, 1fichier.com
	URL         string        `json:"url" bson:"url"`          // the magnet/.torrent/ddl URL
	LinkType    LinkType      `json:"linkType" bson:"link_type"`
	SupportedBy []DebridProvider `json:"supportedBy" bson:"supported_by"` // empty = unsupported by both
	// SourcePublishedAt carries the source post publish date (dlpsgame) through to
	// the Game upsert; 0 when the scraper has no publish date (other sources).
	SourcePublishedAt int64         `json:"sourcePublishedAt,omitempty" bson:"source_published_at,omitempty"`
	UpdatedAt         int64         `json:"updatedAt" bson:"updated_at"`
	CreatedAt         int64         `json:"createdAt" bson:"created_at"`
}

// DebridHost maps a host domain to the providers that support it, cached from the
// live TorBox + Real-Debrid host lists.
type DebridHost struct {
	Host        string           `json:"host" bson:"_id"`
	Providers   []DebridProvider `json:"providers" bson:"providers"`
	UpdatedAt   int64            `json:"updatedAt" bson:"updated_at"`
}

// HealthStatus is the /health response body.
type HealthStatus struct {
	Status       string `json:"status"`
	Type         string `json:"type"`
	ResponseTime int64  `json:"responseTimeMs"`
	Timestamp    string `json:"timestamp"`
}

// RawgStore is a single store link lifted from RAWG's /games/{id} detail
// response (e.g. Steam, Epic, GOG with their per-store URL).
type RawgStore struct {
	Name string `json:"name" bson:"name"`
	URL  string `json:"url,omitempty" bson:"url,omitempty"`
}

// RawgFields is the RAWG enrichment payload the Enricher produces and the mongo
// layer persists onto a Game. Lives in models so the persistence interface can
// reference it without importing the metadata service (no persistence->service
// coupling). The list response covers the matching fields + screenshots; the
// detail response fills description/developers/publishers/metacritic/esrb/
// tags/stores/playtime.
type RawgFields struct {
	ID            int64
	Rating        float64
	Cover         string
	Released      string
	Genres        []string
	Description   string
	Metacritic    *int
	MetacriticURL string
	ESRB          string
	Developers    []string
	Publishers    []string
	Tags          []string
	Stores        []RawgStore
	Screenshots   []string
	Playtime      int
	RedditURL     string
	DetailFetched bool
}