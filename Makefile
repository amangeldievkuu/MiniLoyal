BINARIES    := api worker migrate
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)

# Tools run through "go run" with pinned versions, so nothing needs installing.
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0
SQLC        := github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
OAPI        := github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.2

# Our own packages only: npm packages under node_modules may ship Go files.
GOPKGS      := $(shell go list ./... | grep -v /node_modules/)
# Generated packages are vetted and tested but not style-checked.
LINTPKGS    := $(filter-out %/internal/api %/internal/db/sqlc,$(GOPKGS))
GOFMT_DIRS  := cmd internal db

.PHONY: help build test test-go test-short test-node lint lint-go lint-node vuln gen gen-api gen-sql gen-ts gen-check migrate migrate-status db-up db-down dev dev-dashboard dev-staff ci docker clean

## help: list targets
help:
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':'

## build: static binaries in bin/ (api, worker, migrate)
build:
	@mkdir -p bin
	@for b in $(BINARIES); do echo "build $$b"; CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$$b ./cmd/$$b || exit 1; done

## test: Go tests with the race detector, then workspace tests
test: test-go test-node

test-go:
	go test -race $(GOPKGS)

## test-short: fast Go tests (used by the Claude Stop hook)
test-short:
	go test -short $(GOPKGS)

test-node:
	@if [ -d node_modules ]; then npm test; else echo "test-node: skipped (run npm install)"; fi

## lint: gofmt, go vet, staticcheck, eslint, prettier
lint: lint-go lint-node

lint-go:
	@test -z "$$(gofmt -l $(GOFMT_DIRS))" || (echo "gofmt needed on:"; gofmt -l $(GOFMT_DIRS); exit 1)
	go vet $(GOPKGS)
	go run $(STATICCHECK) $(LINTPKGS)

lint-node:
	@if [ -d node_modules ]; then npm run lint && npm run format:check; else echo "lint-node: skipped (run npm install)"; fi

## vuln: govulncheck
vuln:
	go run $(GOVULNCHECK) $(GOPKGS)

## gen: regenerate server code (oapi-codegen), queries (sqlc) and TS types
gen: gen-api gen-sql gen-ts

gen-api:
	go run $(OAPI) -config api/oapi-codegen.yaml api/openapi.yaml

gen-sql:
	go run $(SQLC) generate

gen-ts:
	@if [ -d node_modules ]; then npm run gen:api; else echo "gen-ts: skipped (run npm install)"; fi

## gen-check: fail when generated code is out of date
gen-check: gen
	@git diff --exit-code -- internal/api internal/db sdk/js/src/api/schema.d.ts web/dashboard/src/api/schema.d.ts web/staff/src/api/schema.d.ts || (echo "generated code is stale: run make gen and commit"; exit 1)

## migrate: apply pending migrations to DATABASE_URL
migrate:
	go run ./cmd/migrate up

migrate-status:
	go run ./cmd/migrate status

## db-up: start Postgres with docker compose and wait for it
db-up:
	docker compose up -d --wait db

db-down:
	docker compose down

## dev: Postgres up, migrations applied, API running with config from .env
dev: db-up migrate
	go run ./cmd/api

dev-dashboard:
	npm run dev -w @miniloyal/dashboard

dev-staff:
	npm run dev -w @miniloyal/staff

## ci: everything CI runs
ci: lint vuln gen-check test

## docker: build the three images
docker:
	docker build --target api -t miniloyal-api .
	docker build --target worker -t miniloyal-worker .
	docker build --target web -t miniloyal-web .

## clean: remove build output
clean:
	rm -rf bin
