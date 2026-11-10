// Package testutil gives integration tests a throwaway PostgreSQL database
// with the roles and migrations applied.
//
// The admin connection comes from TEST_DATABASE_URL (the environment, or the
// repository's .env for local runs). Without it, a postgres:17 container is
// started through testcontainers; without Docker the test is skipped.
// Tests under -short are always skipped.
package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/platform/migrate"
)

// AppPassword and WorkerPassword are the login roles' passwords in test
// databases.
const (
	AppPassword    = "miniloyal_api"
	WorkerPassword = "miniloyal_worker"
)

// rolesLockID serialises role creation across test processes.
const rolesLockID = 72300011

// DB is a database created for one test with every migration applied.
type DB struct {
	// Admin connects as the owner: use it for fixtures and assertions that
	// must see every row.
	Admin *pgxpool.Pool
	// App connects as miniloyal_api, exactly like the API does, so row-level
	// security applies.
	App *pgxpool.Pool
	// Worker connects as miniloyal_worker, exactly like cmd/worker does: it
	// owns River's tables and row-level security still applies.
	Worker *pgxpool.Pool
	// AdminURL, AppURL and WorkerURL are the matching connection strings.
	AdminURL  string
	AppURL    string
	WorkerURL string
	Name      string
	// rootURL is the shared admin database where cross-process locks live.
	rootURL string
}

// NewDB creates a fresh database, applies roles and migrations and registers
// cleanup that drops it. It skips the test when no database is reachable.
func NewDB(t testing.TB) *DB {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: skipped with -short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	adminURL := adminURL(t, ctx)
	name := "miniloyal_test_" + randomHex(6)

	root, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatalf("testutil: open admin: %v", err)
	}
	defer root.Close()
	if _, err := root.ExecContext(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("testutil: create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		root, err := sql.Open("pgx", adminURL)
		if err != nil {
			return
		}
		defer root.Close()
		_, _ = root.ExecContext(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	})

	dbAdminURL := withDatabase(t, adminURL, name)
	owner, err := sql.Open("pgx", dbAdminURL)
	if err != nil {
		t.Fatalf("testutil: open database: %v", err)
	}
	// Roles are cluster-wide. Test packages run in parallel, and concurrent
	// ALTER ROLE statements fail with "tuple concurrently updated", so take an
	// advisory lock on the shared admin database around role setup.
	lock, err := root.Conn(ctx)
	if err != nil {
		t.Fatalf("testutil: lock connection: %v", err)
	}
	if _, err := lock.ExecContext(ctx, "SELECT pg_advisory_lock($1)", rolesLockID); err != nil {
		t.Fatalf("testutil: advisory lock: %v", err)
	}
	rolesErr := migrate.Roles(ctx, owner, migrate.Passwords{API: AppPassword, Worker: WorkerPassword})
	_, _ = lock.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", rolesLockID)
	_ = lock.Close()
	if rolesErr != nil {
		t.Fatalf("testutil: roles: %v", rolesErr)
	}
	// Migration 00009 makes the worker a member of miniloyal_app and 00011
	// takes that back. Membership is cluster-wide, so a fresh database
	// briefly widens the worker's rights in every other test database.
	release := clusterLock(t, ctx, adminURL, false)
	err = migrate.Up(ctx, owner)
	release()
	if err != nil {
		t.Fatalf("testutil: migrate: %v", err)
	}
	_ = owner.Close()

	appURL := withUser(t, dbAdminURL, migrate.LoginRole, AppPassword)
	workerURL := withUser(t, dbAdminURL, migrate.WorkerRole, WorkerPassword)
	adminPool, err := db.Connect(ctx, dbAdminURL)
	if err != nil {
		t.Fatalf("testutil: connect admin: %v", err)
	}
	appPool, err := db.Connect(ctx, appURL)
	if err != nil {
		adminPool.Close()
		t.Fatalf("testutil: connect app: %v", err)
	}
	workerPool, err := db.Connect(ctx, workerURL)
	if err != nil {
		appPool.Close()
		adminPool.Close()
		t.Fatalf("testutil: connect worker: %v", err)
	}
	t.Cleanup(func() {
		workerPool.Close()
		appPool.Close()
		adminPool.Close()
	})
	return &DB{Admin: adminPool, App: appPool, Worker: workerPool, AdminURL: dbAdminURL, AppURL: appURL, WorkerURL: workerURL, Name: name, rootURL: adminURL}
}

// workerRoleLockID guards the worker role's cluster-wide membership in
// miniloyal_app. Migrations that grant it for a moment (00009 before 00011
// revokes it, or 00011 rolled back) hold the lock shared; tests that assert
// what the worker may not do hold it exclusively, so they never observe
// another database's migration in flight.
const workerRoleLockID = 72300012

// clusterLock takes the worker role lock on the shared admin database,
// where every test process meets, and returns its release.
func clusterLock(t testing.TB, ctx context.Context, rootURL string, exclusive bool) func() {
	t.Helper()
	root, err := sql.Open("pgx", rootURL)
	if err != nil {
		t.Fatalf("testutil: open admin: %v", err)
	}
	conn, err := root.Conn(ctx)
	if err != nil {
		_ = root.Close()
		t.Fatalf("testutil: lock connection: %v", err)
	}
	lock, unlock := "pg_advisory_lock_shared", "pg_advisory_unlock_shared"
	if exclusive {
		lock, unlock = "pg_advisory_lock", "pg_advisory_unlock"
	}
	if _, err := conn.ExecContext(ctx, "SELECT "+lock+"($1)", workerRoleLockID); err != nil {
		_ = conn.Close()
		_ = root.Close()
		t.Fatalf("testutil: worker role lock: %v", err)
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT "+unlock+"($1)", workerRoleLockID)
		_ = conn.Close()
		_ = root.Close()
	}
}

// WithSteadyWorkerRole runs fn while no test database is between
// migrations that change the worker role's cluster-wide membership. Use it
// around assertions about what the worker may not do. fn must not call
// NewDB.
func (d *DB) WithSteadyWorkerRole(t testing.TB, fn func()) {
	t.Helper()
	release := clusterLock(t, context.Background(), d.rootURL, true)
	defer release()
	fn()
}

// WhileChangingWorkerRole runs fn, which migrates down past 00011 and back
// up, so that WithSteadyWorkerRole waits for it.
func (d *DB) WhileChangingWorkerRole(t testing.TB, fn func()) {
	t.Helper()
	release := clusterLock(t, context.Background(), d.rootURL, false)
	defer release()
	fn()
}

// WithTenant runs fn as the application role inside tenantID's RLS context.
func (d *DB) WithTenant(t testing.TB, tenantID uuid.UUID, fn db.TxFunc) error {
	t.Helper()
	return db.WithTenant(context.Background(), d.App, tenantID, fn)
}

// WithSystem runs fn as the application role with tenant policies bypassed.
func (d *DB) WithSystem(t testing.TB, fn db.TxFunc) error {
	t.Helper()
	return db.WithSystem(context.Background(), d.App, fn)
}

// Exec runs sql as the owner and fails the test on error.
func (d *DB) Exec(t testing.TB, sql string, args ...any) {
	t.Helper()
	if _, err := d.Admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("testutil: exec %q: %v", sql, err)
	}
}

// NewTenant inserts a tenant as the owner and returns its ID.
func (d *DB) NewTenant(t testing.TB, name string) uuid.UUID {
	t.Helper()
	id := NewID()
	d.Exec(t, "INSERT INTO tenants (id, type, name, slug) VALUES ($1, 'brand', $2, $3)", id, name, strings.ToLower(name)+"-"+randomHex(3))
	return id
}

// NewID returns a UUID v7 the way the services do.
func NewID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

var (
	containerOnce sync.Once
	containerURL  string
	containerErr  error
)

// adminURL finds an owner connection string: TEST_DATABASE_URL from the
// environment or .env, else a shared testcontainers Postgres.
func adminURL(t testing.TB, ctx context.Context) string {
	t.Helper()
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	if root := repoRoot(); root != "" {
		_ = config.LoadDotEnv(filepath.Join(root, ".env"))
		if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
			return v
		}
	}
	containerOnce.Do(func() {
		c, err := postgres.Run(ctx, "postgres:17",
			postgres.WithDatabase("miniloyal"),
			postgres.WithUsername("miniloyal"),
			postgres.WithPassword("miniloyal"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
		)
		if err != nil {
			containerErr = err
			return
		}
		containerURL, containerErr = c.ConnectionString(ctx, "sslmode=disable")
	})
	if containerErr != nil {
		t.Skipf("integration test: no TEST_DATABASE_URL and no Docker (%v)", containerErr)
	}
	return containerURL
}

func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func withDatabase(t testing.TB, raw, name string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("testutil: bad TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

func withUser(t testing.TB, raw, user, password string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("testutil: bad URL: %v", err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("testutil: random: %v", err))
	}
	return hex.EncodeToString(b)
}
