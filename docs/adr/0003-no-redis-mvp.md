# ADR 0003: No Redis in the MVP

**Status:** Accepted, 2026-11-09

## Context

The v1 plan listed Redis for sessions, caching and rate limits next to PostgreSQL and a
Postgres-backed job queue (River). The MVP runs on one VM in Docker Compose (ADR 0005) with a
handful of pilot apps and tens of thousands of members at most. Every extra service is another
container to back up, monitor and secure.

## Decision

- Member sessions are stateless 1-hour JWTs. Dashboard refresh tokens live in Postgres as
  SHA-256 hashes. No session store is needed.
- Rate limits use in-process token buckets per API instance, with Postgres counters for the
  limits that must be global (per API key, per member). With one `api` replica the in-process
  limiter is exact.
- Idempotency keys, webhook dedupe and the job queue (River) are Postgres tables.
- LINE JWKS and stateless channel access tokens are cached in process memory.
- Redis is added only when a measurement shows Postgres or the in-process caches are the
  bottleneck, or when `api` runs as several replicas and per-IP limits must be shared.

## Consequences

- One database to operate, back up and restore. Fewer secrets and ports.
- A horizontal scale-out of `api` later needs either a shared limiter (Redis or Postgres) or
  acceptance of per-replica limits. The limiter sits behind an interface so the swap is local.
- JWKS and token caches are per replica; a cold replica makes one extra call to LINE.
