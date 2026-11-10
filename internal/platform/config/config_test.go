package config

import (
	"os"
	"path/filepath"
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
	isolatePG(t)
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
	isolatePG(t)
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

// isolatePG clears the libpq variables pgx reads while parsing a connection
// string, so the developer's own environment cannot change the outcome.
func isolatePG(t *testing.T) {
	t.Helper()
	for _, k := range []string{"PGPASSWORD", "PGUSER", "PGHOST", "PGSERVICE", "PGSERVICEFILE"} {
		t.Setenv(k, "")
	}
	t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "none"))
}

// The development passwords of the login roles and the compose superuser
// are public, so staging and production refuse them, whether set directly or
// reaching a connection through a URL, a key=value string, PGPASSWORD or a
// passfile.
func TestFromEnvRefusesDefaultPasswordsOutsideDevelopment(t *testing.T) {
	isolatePG(t)
	base := map[string]string{"ENV": "production", "DATABASE_URL": "postgres://miniloyal_api:s3cret@db/x",
		"SESSION_SIGNING_KEY": "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=", "KEK_FILE": "/k"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for key, val := range base {
			m[key] = val
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	if _, err := FromEnv(lookup(base)); err != nil {
		t.Fatalf("production with real passwords: %v", err)
	}
	passfile := filepath.Join(t.TempDir(), "pgpass")
	if err := os.WriteFile(passfile, []byte("*:*:*:miniloyal:miniloyal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		env  map[string]string
		pg   map[string]string
	}{
		{"app password", with("APP_DB_PASSWORD", "miniloyal_api"), nil},
		{"worker password", with("WORKER_DB_PASSWORD", "miniloyal_worker"), nil},
		{"owner password as app password", with("APP_DB_PASSWORD", "miniloyal"), nil},
		{"api url", with("DATABASE_URL", "postgres://miniloyal_api:miniloyal_api@db/x"), nil},
		{"worker url", with("WORKER_DATABASE_URL", "postgres://miniloyal_worker:miniloyal_worker@db/x"), nil},
		{"migrate url", with("MIGRATE_DATABASE_URL", "postgres://miniloyal:miniloyal@db/x"), nil},
		{"owner url as DATABASE_URL", with("DATABASE_URL", "postgres://miniloyal:miniloyal@db/x"), nil},
		{"key=value string", with("DATABASE_URL", "host=db user=miniloyal_api password=miniloyal_api dbname=x"), nil},
		{"key=value migrate string", with("MIGRATE_DATABASE_URL", "host=db user=miniloyal password=miniloyal"), nil},
		{"PGPASSWORD", with("MIGRATE_DATABASE_URL", "postgres://miniloyal@db/x"), map[string]string{"PGPASSWORD": "miniloyal"}},
		{"passfile", with("MIGRATE_DATABASE_URL", "postgres://miniloyal@db/x"), map[string]string{"PGPASSFILE": passfile}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.pg {
				t.Setenv(k, v)
			}
			for _, e := range []string{"staging", "production"} {
				tc.env["ENV"] = e
				if _, err := FromEnv(lookup(tc.env)); err == nil || !strings.Contains(err.Error(), "development default") {
					t.Errorf("%s: %v, want a refusal", e, err)
				}
			}
		})
	}

	// A string pgx cannot parse is refused without echoing it, since it may
	// hold a password.
	_, err := FromEnv(lookup(with("WORKER_DATABASE_URL", "postgres://u:hunter2@%zz/x")))
	if err == nil || !strings.Contains(err.Error(), "WORKER_DATABASE_URL is not a valid connection string") || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("unparseable URL: %v", err)
	}

	dev := with("ENV", "development", "DATABASE_URL", "postgres://miniloyal_api:miniloyal_api@db/x",
		"MIGRATE_DATABASE_URL", "postgres://miniloyal:miniloyal@db/x")
	if _, err := FromEnv(lookup(dev)); err != nil {
		t.Errorf("development keeps its defaults: %v", err)
	}
}

func TestFromEnvCouponRateLimits(t *testing.T) {
	cfg, err := FromEnv(lookup(map[string]string{"DATABASE_URL": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CouponClaimsPerMinute != 10 || cfg.CouponAPIIssuesPerMinute != 300 {
		t.Errorf("defaults: claims %d, api issues %d", cfg.CouponClaimsPerMinute, cfg.CouponAPIIssuesPerMinute)
	}
	cfg, err = FromEnv(lookup(map[string]string{"DATABASE_URL": "x", "COUPON_CLAIMS_PER_MINUTE": "3", "COUPON_API_ISSUES_PER_MINUTE": "50"}))
	if err != nil || cfg.CouponClaimsPerMinute != 3 || cfg.CouponAPIIssuesPerMinute != 50 {
		t.Errorf("overrides: %+v, %v", cfg, err)
	}
}

func TestFromEnvRejectsBadValues(t *testing.T) {
	cases := map[string]map[string]string{
		"env":         {"DATABASE_URL": "x", "ENV": "prod"},
		"db":          {},
		"concurrency": {"DATABASE_URL": "x", "WORKER_CONCURRENCY": "0"},
		"claims":      {"DATABASE_URL": "x", "COUPON_CLAIMS_PER_MINUTE": "0"},
		"api issues":  {"DATABASE_URL": "x", "COUPON_API_ISSUES_PER_MINUTE": "many"},
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
