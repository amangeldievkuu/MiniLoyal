package config

import (
	"strings"
	"testing"
)

func lookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestFromEnvDevelopmentDefaults(t *testing.T) {
	cfg, err := FromEnv(lookup(map[string]string{"DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Env != Development || cfg.APIAddr != ":8080" || cfg.WorkerConcurrency != 4 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if len(cfg.SessionSigningKey) < 32 || cfg.KEKFile != ".kek" {
		t.Errorf("development fallbacks missing: %+v", cfg)
	}
}

// The worker's environment carries only WORKER_DATABASE_URL; nothing falls
// back from one role's URL to the other's.
func TestFromEnvDatabaseURLs(t *testing.T) {
	cfg, err := FromEnv(lookup(map[string]string{"WORKER_DATABASE_URL": "postgres://worker"}))
	if err != nil {
		t.Fatalf("worker-only environment: %v", err)
	}
	if cfg.WorkerDatabaseURL != "postgres://worker" || cfg.DatabaseURL != "" {
		t.Errorf("worker-only URLs: %+v", cfg)
	}
	if cfg.AppDBPassword != "miniloyal_api" || cfg.WorkerDBPassword != "miniloyal_worker" {
		t.Errorf("development passwords: api %q, worker %q", cfg.AppDBPassword, cfg.WorkerDBPassword)
	}

	cfg, err = FromEnv(lookup(map[string]string{"DATABASE_URL": "postgres://api"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkerDatabaseURL != "" {
		t.Errorf("WorkerDatabaseURL fell back to %q", cfg.WorkerDatabaseURL)
	}

	if _, err := FromEnv(lookup(map[string]string{})); err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Errorf("no database URL: %v", err)
	}

	cfg, err = FromEnv(lookup(map[string]string{"ENV": "production", "DATABASE_URL": "postgres://api",
		"SESSION_SIGNING_KEY": "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=", "KEK_FILE": "/k"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppDBPassword != "" || cfg.WorkerDBPassword != "" {
		t.Errorf("production must not default passwords: api %q, worker %q", cfg.AppDBPassword, cfg.WorkerDBPassword)
	}
}

func TestFromEnvProductionRequiresSecrets(t *testing.T) {
	_, err := FromEnv(lookup(map[string]string{"ENV": "production", "DATABASE_URL": "postgres://x"}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"SESSION_SIGNING_KEY", "KEK_FILE or KMS_KEY_NAME"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}

func TestFromEnvRejectsBadValues(t *testing.T) {
	cases := map[string]map[string]string{
		"env":         {"DATABASE_URL": "x", "ENV": "prod"},
		"db":          {},
		"concurrency": {"DATABASE_URL": "x", "WORKER_CONCURRENCY": "0"},
		"key-b64":     {"DATABASE_URL": "x", "SESSION_SIGNING_KEY": "not base64!"},
		"key-short":   {"DATABASE_URL": "x", "SESSION_SIGNING_KEY": "c2hvcnQ="},
	}
	for name, env := range cases {
		if _, err := FromEnv(lookup(env)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseValue(t *testing.T) {
	cases := map[string]string{
		`plain`:              "plain",
		`"quoted value"`:     "quoted value",
		`'single' # comment`: "single",
		`value # trailing`:   "value",
		`"keep # inside"`:    "keep # inside",
		`  spaced  `:         "spaced",
		`"unterminated`:      `"unterminated`,
	}
	for in, want := range cases {
		if got := parseValue(in); got != want {
			t.Errorf("parseValue(%q) = %q, want %q", in, got, want)
		}
	}
}
