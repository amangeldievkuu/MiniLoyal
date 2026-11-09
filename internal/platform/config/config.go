// Package config loads the service configuration from the environment and an
// optional .env file. Every binary (api, worker, migrate) uses the same Config.
package config

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env is the deployment environment.
type Env string

// Deployment environments.
const (
	Development Env = "development"
	Staging     Env = "staging"
	Production  Env = "production"
)

// Config holds every setting the services read. Fields are plain values so
// they can be constructed directly in tests.
type Config struct {
	Env             Env
	APIAddr         string
	DatabaseURL     string
	LogLevel        string
	LogFormat       string
	PublicBaseURL   string
	DashboardOrigin string

	// SessionSigningKey signs platform session tokens. Required outside
	// development; development derives a fixed key so local runs just work.
	SessionSigningKey []byte

	// Exactly one of KEKFile (development) or KMSKeyName (production) wraps
	// the data-encryption keys that protect LINE channel credentials.
	KEKFile    string
	KMSKeyName string

	WorkerConcurrency int
	OTLPEndpoint      string
	ShutdownTimeout   time.Duration
}

// Load reads .env (existing environment variables win), then the environment,
// and validates the result.
func Load() (*Config, error) {
	if err := LoadDotEnv(".env"); err != nil {
		return nil, err
	}
	return FromEnv(os.LookupEnv)
}

// FromEnv builds a Config from a lookup function, so tests can pass a map.
func FromEnv(lookup func(string) (string, bool)) (*Config, error) {
	get := func(key, def string) string {
		if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	cfg := &Config{
		Env:             Env(get("ENV", string(Development))),
		APIAddr:         get("API_ADDR", ":8080"),
		DatabaseURL:     get("DATABASE_URL", ""),
		LogLevel:        get("LOG_LEVEL", "info"),
		LogFormat:       get("LOG_FORMAT", "json"),
		PublicBaseURL:   get("PUBLIC_BASE_URL", "http://localhost:8080"),
		DashboardOrigin: get("DASHBOARD_ORIGIN", "http://localhost:5173"),
		KEKFile:         get("KEK_FILE", ""),
		KMSKeyName:      get("KMS_KEY_NAME", ""),
		OTLPEndpoint:    get("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		ShutdownTimeout: 15 * time.Second,
	}

	var errs []error
	switch cfg.Env {
	case Development, Staging, Production:
	default:
		errs = append(errs, fmt.Errorf("ENV must be development, staging or production, got %q", cfg.Env))
	}
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}

	n, err := strconv.Atoi(get("WORKER_CONCURRENCY", "4"))
	if err != nil || n < 1 {
		errs = append(errs, errors.New("WORKER_CONCURRENCY must be a positive integer"))
	}
	cfg.WorkerConcurrency = n

	if raw := get("SESSION_SIGNING_KEY", ""); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			errs = append(errs, errors.New("SESSION_SIGNING_KEY must be base64"))
		} else if len(key) < 32 {
			errs = append(errs, errors.New("SESSION_SIGNING_KEY must decode to at least 32 bytes"))
		}
		cfg.SessionSigningKey = key
	} else if cfg.Env == Development {
		cfg.SessionSigningKey = []byte("miniloyal-development-only-signing-key!!")
	} else {
		errs = append(errs, errors.New("SESSION_SIGNING_KEY is required outside development"))
	}

	if cfg.Env != Development {
		if (cfg.KEKFile == "") == (cfg.KMSKeyName == "") {
			errs = append(errs, errors.New("set exactly one of KEK_FILE or KMS_KEY_NAME"))
		}
	} else if cfg.KEKFile == "" && cfg.KMSKeyName == "" {
		cfg.KEKFile = ".kek"
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// LoadDotEnv sets environment variables from a .env file of KEY=VALUE lines.
// Variables already set in the environment win, and a missing file is not an
// error.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
		}
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, parseValue(value)); err != nil {
			return fmt.Errorf("%s:%d: %w", path, n, err)
		}
	}
	return sc.Err()
}

// parseValue strips matching quotes, or a trailing " # comment" from an
// unquoted value. A quoted value may also be followed by a comment.
func parseValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]) + 1; end > 0 {
			if rest := strings.TrimSpace(v[end+1:]); rest == "" || strings.HasPrefix(rest, "#") {
				return v[1:end]
			}
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}
