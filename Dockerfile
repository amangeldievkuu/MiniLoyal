# syntax=docker/dockerfile:1
# Targets: api, worker, migrate (Go, distroless) and web (dashboard behind nginx).
#   docker compose --profile app up -d --build

FROM golang:1.27-bookworm AS gobuild
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ ./cmd/api ./cmd/worker ./cmd/migrate

# API: no shell in the image, so the binary probes itself for the health check.
FROM gcr.io/distroless/static-debian12:nonroot AS api
COPY --from=gobuild /out/api /api
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=3 CMD ["/api", "-healthcheck"]
ENTRYPOINT ["/api"]

FROM gcr.io/distroless/static-debian12:nonroot AS worker
COPY --from=gobuild /out/worker /worker
ENTRYPOINT ["/worker"]

FROM gcr.io/distroless/static-debian12:nonroot AS migrate
COPY --from=gobuild /out/migrate /migrate
ENTRYPOINT ["/migrate"]
CMD ["up"]

# Dashboard: Vite build served by nginx, with /v1 proxied to the API (same origin).
FROM node:22.22-bookworm-slim AS webbuild
WORKDIR /src
COPY package.json package-lock.json ./
COPY sdk/js/package.json sdk/js/
COPY web/dashboard/package.json web/dashboard/
COPY web/staff/package.json web/staff/
COPY apps/demo-cafe/package.json apps/demo-cafe/
RUN npm ci
COPY api ./api
COPY tsconfig.base.json ./
COPY sdk ./sdk
COPY web ./web
COPY apps ./apps
RUN npm run build -w @miniloyal/sdk && npm run build -w @miniloyal/dashboard

FROM nginx:1.27-alpine AS web
COPY deploy/nginx.conf /etc/nginx/conf.d/default.conf
COPY deploy/security-headers.conf /etc/nginx/security-headers.conf
COPY --from=webbuild /src/web/dashboard/dist /usr/share/nginx/html
EXPOSE 80
