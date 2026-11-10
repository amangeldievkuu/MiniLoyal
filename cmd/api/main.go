// Command api serves the MiniLoyal HTTP API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/amangeldievkuu/miniloyal/internal/card"
	"github.com/amangeldievkuu/miniloyal/internal/points"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/amangeldievkuu/miniloyal/internal/auth"
	"github.com/amangeldievkuu/miniloyal/internal/httpserver"
	"github.com/amangeldievkuu/miniloyal/internal/line"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
	"github.com/amangeldievkuu/miniloyal/internal/member"
	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
	"github.com/amangeldievkuu/miniloyal/internal/platform/config"
	"github.com/amangeldievkuu/miniloyal/internal/platform/crypto"
	"github.com/amangeldievkuu/miniloyal/internal/platform/db"
	"github.com/amangeldievkuu/miniloyal/internal/platform/log"
	"github.com/amangeldievkuu/miniloyal/internal/platform/otel"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
	"github.com/amangeldievkuu/miniloyal/internal/webhook"
)

// version is set at build time: -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	healthcheck := flag.Bool("healthcheck", false, "probe the running server's /healthz and exit (for container health checks)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return 0
	}
	if *healthcheck {
		return probe()
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	logger, err := log.Setup(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := otel.Setup(ctx, "miniloyal-api", version, cfg.OTLPEndpoint)
	if err != nil {
		logger.Error("tracing setup failed", "err", err)
		return 1
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connection failed", "err", err)
		return 1
	}
	defer pool.Close()

	keyring, err := crypto.FromConfig(cfg)
	if err != nil {
		logger.Error("keyring setup failed", "err", err)
		return 1
	}
	authSvc, err := auth.New(auth.Deps{
		Pool:      pool,
		Clock:     clock.Real{},
		Log:       logger,
		Encryptor: keyring,
		Config:    auth.Config{SigningKey: cfg.SessionSigningKey},
	})
	if err != nil {
		logger.Error("auth service setup failed", "err", err)
		return 1
	}
	tenants := tenant.New(pool, keyring, clock.Real{}, logger)

	// LINE Platform clients. Credentials are proven against LINE before they
	// are stored, webhooks are verified with the stored secret, and member
	// sign-in verifies LIFF ID tokens against LINE's JWKS.
	lineHTTP := &http.Client{Timeout: 5 * time.Second}
	lineTokens := line.NewTokenSource(line.TokenSourceOptions{HTTPClient: lineHTTP, Clock: clock.Real{}})
	lineMessaging := line.NewMessaging(line.MessagingOptions{HTTPClient: lineHTTP, Tokens: lineTokens})
	if cfg.LineSkipCredentialVerification {
		logger.Warn("LINE credential verification is disabled (LINE_SKIP_CREDENTIAL_VERIFICATION); credentials are stored unverified")
	} else {
		tenants.SetChannelVerifier(line.NewChannelVerifier(lineTokens, lineMessaging))
	}
	idTokens := line.NewVerifier(line.VerifierOptions{
		// No remote verify fallback: LIFF tokens are ES256 and verifiable
		// locally; an unknown kid after a fresh JWKS is simply a bad token.
		HTTPClient: lineHTTP, Clock: clock.Real{}, Log: logger, VerifyEndpointFallback: false,
	})
	webhooks := webhook.New(pool, tenants, logger)

	lineAuth, err := lineauth.New(lineauth.Deps{
		Pool:     pool,
		Keyring:  keyring,
		Verifier: lineVerifier{v: idTokens},
		Clock:    clock.Real{},
		Log:      logger,
		Config:   lineauth.Config{SigningKey: cfg.SessionSigningKey, RequireVerifiedChannels: !cfg.LineSkipCredentialVerification},
	})
	if err != nil {
		logger.Error("line auth setup failed", "err", err)
		return 1
	}
	cards, err := card.New(cfg.SessionSigningKey, clock.Real{})
	if err != nil {
		logger.Error("card codec setup failed", "err", err)
		return 1
	}
	pointsSvc := points.New(points.Deps{Pool: pool, Tenants: tenants, Keyring: keyring, Card: cards, Clock: clock.Real{}, Log: logger})
	members := member.New(member.Deps{Pool: pool, Tenants: tenants, Keyring: keyring, Clock: clock.Real{}, Log: logger})

	srv := &http.Server{
		Addr: cfg.APIAddr,
		Handler: httpserver.New(httpserver.Deps{
			Config: cfg, Log: logger, DB: pool, Version: version,
			Auth: authSvc, Dashboard: authSvc,
			APIKeys:   tenants,
			Tenants:   tenants,
			Members:   lineAuth,
			LineAuth:  lineAuth,
			MemberSvc: members,
			Webhooks:  webhooks,
			Card:      cards,
			Points:    pointsSvc,
		}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api listening", "addr", cfg.APIAddr, "env", cfg.Env, "version", version)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			return 1
		}
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "err", err)
		return 1
	}
	return 0
}

// probe is the container health check: distroless images have no shell or
// curl, so the binary checks itself.
func probe() int {
	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck: bad API_ADDR:", err)
		return 1
	}
	if host == "" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.Status)
		return 1
	}
	return 0
}
