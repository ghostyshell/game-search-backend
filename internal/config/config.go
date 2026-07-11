// Package config loads game-search-backend configuration from environment.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all configuration for the application.
type Config struct {
	Environment   string               `json:"environment"`
	IsDevelopment bool                 `json:"isDevelopment"`
	IsProduction  bool                 `json:"isProduction"`
	Server        ServerConfig         `json:"server"`
	CORS          CORSConfig           `json:"cors"`
	Database      DatabaseConfig       `json:"database"`
	Metadata      MetadataConfig       `json:"metadata"`
	BackgroundJobs BackgroundJobsConfig `json:"backgroundJobs"`
	FrontendURL   string               `json:"frontendUrl"`
}

type ServerConfig struct {
	Port int    `json:"port"`
	Host string `json:"host"`
}

type CORSConfig struct {
	Origins        []string `json:"origins"`
	Credentials    bool     `json:"credentials"`
	Methods        []string `json:"methods"`
	AllowedHeaders []string `json:"allowedHeaders"`
}

type DatabaseConfig struct {
	Mongo MongoConfig `json:"mongo"`
}

type MongoConfig struct {
	URI    string `json:"uri"`
	DBName string `json:"dbName"`
}

// MetadataConfig holds RAWG + TheGamesDB API keys (optional; enrich degrades gracefully).
// RAWG supports a key pool (RAWG_API_KEYS, comma-separated) for rotating around
// the per-key request limit; a bare RAWG_API_KEY is appended if set.
type MetadataConfig struct {
	RAWGAPIKeys       []string `json:"rawgApiKeys"`
	TheGamesDBAPIKey  string   `json:"thegamesdbApiKey"`
	RAWGAPIURL        string   `json:"rawgApiUrl"`
	TheGamesDBAPIURL  string   `json:"thegamesdbApiUrl"`
}

// JobScheduleConfig holds interval + initial delay for a periodic job.
type JobScheduleConfig struct {
	Interval     time.Duration `json:"interval"`
	InitialDelay time.Duration `json:"initialDelay"`
}

// BackgroundJobsConfig groups tunables for all periodic ingest/sync jobs.
type BackgroundJobsConfig struct {
	DebridHostsSync JobScheduleConfig `json:"debridHostsSync"`
	MinervaSync     JobScheduleConfig `json:"minervaSync"`
	FitGirlSync     JobScheduleConfig `json:"fitGirlSync"`
	RRomsSync       JobScheduleConfig `json:"rromsSync"`
	DLPsGameSync    JobScheduleConfig `json:"dlpsGameSync"`
	NSWGameSync     JobScheduleConfig `json:"nswGameSync"`
	MetaEnricher    JobScheduleConfig `json:"metaEnricher"`
}

// Load loads configuration from environment variables.
func Load() (*Config, error) {
	isProduction := os.Getenv("NODE_ENV") == "production" || os.Getenv("ENVIRONMENT") == "production"
	isDevelopment := !isProduction
	envName := getEnv("ENVIRONMENT", getEnv("NODE_ENV", "development"))

	cfg := &Config{
		Environment:   envName,
		IsDevelopment: isDevelopment,
		IsProduction:  isProduction,
		Server: ServerConfig{
			Port: getEnvAsInt("PORT", 3011),
			Host: getEnv("HOST", "0.0.0.0"),
		},
		CORS: CORSConfig{
			Origins:     getCorsOrigins(isDevelopment),
			Credentials: true,
			Methods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowedHeaders: []string{
				"Origin", "X-Requested-With", "Content-Type", "Accept", "Authorization",
			},
		},
		Database: DatabaseConfig{
			Mongo: MongoConfig{
				URI:    buildMongoURI(),
				DBName: getEnv("MONGODB_DB", "game_search"),
			},
		},
		Metadata: MetadataConfig{
			RAWGAPIKeys:      parseRawgKeys(os.Getenv("RAWG_API_KEYS"), os.Getenv("RAWG_API_KEY")),
			TheGamesDBAPIKey: os.Getenv("THEGAMESDB_API_KEY"),
			RAWGAPIURL:       strings.TrimSuffix(getEnv("RAWG_API_URL", "https://api.rawg.io/api"), "/"),
			TheGamesDBAPIURL: strings.TrimSuffix(getEnv("THEGAMESDB_API_URL", "https://api.thegamesdb.net/v1.1"), "/"),
		},
		BackgroundJobs: BackgroundJobsConfig{
			DebridHostsSync: JobScheduleConfig{
				Interval:     time.Duration(getMaxInt64(60*1000, getEnvAsInt64("DEBRID_HOSTS_SYNC_INTERVAL_MS", 6*60*60*1000))) * time.Millisecond,
				InitialDelay: time.Duration(getMaxInt64(0, getEnvAsInt64("DEBRID_HOSTS_SYNC_INITIAL_MS", 30*1000))) * time.Millisecond,
			},
			MinervaSync: JobScheduleConfig{
				Interval:     time.Duration(getMaxInt64(60*1000, getEnvAsInt64("MINERVA_SYNC_INTERVAL_MS", 12*60*60*1000))) * time.Millisecond,
				InitialDelay: time.Duration(getMaxInt64(0, getEnvAsInt64("MINERVA_SYNC_INITIAL_MS", 60*1000))) * time.Millisecond,
			},
			FitGirlSync: JobScheduleConfig{
				Interval:     time.Duration(getMaxInt64(60*1000, getEnvAsInt64("FITGIRL_SYNC_INTERVAL_MS", 12*60*60*1000))) * time.Millisecond,
				InitialDelay: time.Duration(getMaxInt64(0, getEnvAsInt64("FITGIRL_SYNC_INITIAL_MS", 90*1000))) * time.Millisecond,
			},
			RRomsSync: JobScheduleConfig{
				Interval:     time.Duration(getMaxInt64(60*1000, getEnvAsInt64("RROMS_SYNC_INTERVAL_MS", 24*60*60*1000))) * time.Millisecond,
				InitialDelay: time.Duration(getMaxInt64(0, getEnvAsInt64("RROMS_SYNC_INITIAL_MS", 150*1000))) * time.Millisecond,
			},
			DLPsGameSync: JobScheduleConfig{
				Interval:     time.Duration(getMaxInt64(60*1000, getEnvAsInt64("DLPSGAME_SYNC_INTERVAL_MS", 12*60*60*1000))) * time.Millisecond,
				InitialDelay: time.Duration(getMaxInt64(0, getEnvAsInt64("DLPSGAME_SYNC_INITIAL_MS", 210*1000))) * time.Millisecond,
			},
			NSWGameSync: JobScheduleConfig{
				Interval:     time.Duration(getMaxInt64(60*1000, getEnvAsInt64("NSWGAME_SYNC_INTERVAL_MS", 12*60*60*1000))) * time.Millisecond,
				InitialDelay: time.Duration(getMaxInt64(0, getEnvAsInt64("NSWGAME_SYNC_INITIAL_MS", 180*1000))) * time.Millisecond,
			},
			MetaEnricher: JobScheduleConfig{
				Interval:     time.Duration(getMaxInt64(10*1000, getEnvAsInt64("META_ENRICHER_INTERVAL_MS", 10*60*1000))) * time.Millisecond,
				InitialDelay: time.Duration(getMaxInt64(0, getEnvAsInt64("META_ENRICHER_INITIAL_MS", 5*1000))) * time.Millisecond,
			},
		},
		FrontendURL: getEnv("FRONTEND_URL", "http://localhost:3010"),
	}

	return cfg, nil
}

func getCorsOrigins(isDevelopment bool) []string {
	if isDevelopment {
		origins := []string{
			"http://localhost:3010",
			"http://127.0.0.1:3010",
		}
		if frontendURL := os.Getenv("FRONTEND_URL"); frontendURL != "" {
			origins = append(origins, frontendURL)
		}
		return origins
	}
	var origins []string
	if frontendURL := os.Getenv("FRONTEND_URL"); frontendURL != "" {
		origins = append(origins, frontendURL)
	}
	if extra := os.Getenv("CORS_ALLOWED_ORIGINS"); extra != "" {
		for _, o := range strings.Split(extra, ",") {
			if o = strings.TrimSpace(o); o != "" {
				origins = append(origins, o)
			}
		}
	}
	if len(origins) == 0 {
		// Fail-closed: an unconfigured prod deployment denies cross-origin
		// requests rather than echoing a wildcard. The frontend origin must be
		// explicitly allowlisted via FRONTEND_URL / CORS_ALLOWED_ORIGINS.
		return nil
	}
	return origins
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultValue
}

func getEnvAsInt64(key string, defaultValue int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return defaultValue
}

func getMaxInt(min, value int) int {
	if value < min {
		return min
	}
	return value
}

func getMaxInt64(min, value int64) int64 {
	if value < min {
		return min
	}
	return value
}

// parseRawgKeys builds the RAWG key pool from RAWG_API_KEYS (comma-separated)
// plus a bare RAWG_API_KEY if set. Dedupes and trims.
func parseRawgKeys(pool, single string) []string {
	var out []string
	seen := make(map[string]struct{})
	add := func(k string) {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] != struct{}{} {
			return
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	for _, k := range strings.Split(pool, ",") {
		add(k)
	}
	add(single)
	return out
}

// buildMongoURI constructs MongoDB URI from env (matches Node buildMongoUri + the frontend).
func buildMongoURI() string {
	base := os.Getenv("MONGODB_URI")
	if base == "" {
		base = os.Getenv("MONGO_URL")
	}
	if base == "" {
		return ""
	}
	user := os.Getenv("MONGO_USERNAME")
	if user == "" {
		user = os.Getenv("MONGO_USER")
	}
	pass := os.Getenv("MONGO_PASSWORD")
	if pass == "" {
		pass = os.Getenv("MONGO_PASS")
	}
	if user == "" || pass == "" {
		return base
	}
	lower := strings.ToLower(base)
	if !strings.HasPrefix(lower, "mongodb://") && !strings.HasPrefix(lower, "mongodb+srv://") {
		return base
	}
	if strings.Contains(base, "@") {
		return base
	}
	schemeEnd := strings.Index(base, "://")
	if schemeEnd < 0 {
		return base
	}
	scheme := base[:schemeEnd+3]
	rest := base[schemeEnd+3:]
	return scheme + url.QueryEscape(user) + ":" + url.QueryEscape(pass) + "@" + rest
}

// Validate returns hard errors (block startup) and warnings (log only).
func (c *Config) Validate() (errors, warnings []error) {
	if c.Database.Mongo.URI == "" {
		errors = append(errors, fmt.Errorf("MONGODB_URI (or MONGO_URL) is required"))
	}
	if len(c.Metadata.RAWGAPIKeys) == 0 {
		warnings = append(warnings, fmt.Errorf("RAWG_API_KEYS not set; RAWG enrichment will be skipped (provide multiple keys to rotate around the per-key limit)"))
	}
	if c.Metadata.TheGamesDBAPIKey == "" {
		warnings = append(warnings, fmt.Errorf("THEGAMESDB_API_KEY not set; TheGamesDB enrichment will be skipped (works keyless at lower limits)"))
	}
	return errors, warnings
}