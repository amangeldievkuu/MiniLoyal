# ADR 0005: One Taiwan VM with Docker Compose for the MVP, managed cloud later

**Status:** Accepted, 2026-11-09

## Context

Member data must stay in Taiwan. The v1 plan proposed Cloud Run plus Cloud SQL from day one.
The team has no Cloud Run or Terraform experience yet, but it runs a production service with a
multi-stage Dockerfile, Docker Compose, nginx, a Postgres backup sidecar and restore scripts.
The pilot has a few apps and low traffic; the ledger needs durable, recoverable storage more
than it needs autoscaling.

## Decision

- One GCP Compute Engine VM in `asia-east1` (Changhua, Taiwan) runs Docker Compose with these
  services: `nginx` (TLS via certbot, security headers, reverse proxy to `api`, static dashboard
  and staff app), `api`, `worker` (River), `db` (`postgres:17`, bound to 127.0.0.1,
  `synchronous_commit=on`), `backup` (daily `pg_dump`, kept 14 days).
- Point-in-time recovery: WAL archiving from `db` to a GCS bucket in `asia-east1` using
  `wal-g` (or `pgbackrest`) in a sidecar, plus a weekly base backup. Dumps are also copied to
  the bucket. A restore drill is part of Phase 7 and repeated quarterly.
- Secrets: channel secrets are envelope-encrypted with data keys wrapped by Cloud KMS; the VM's
  service account has `cloudkms.cryptoKeyEncrypterDecrypter` only. Compose `.env` holds the
  database password and the KMS key name, nothing from LINE in plaintext.
- Deploy: GitHub Actions builds images on `main` and deploys to the staging VM over SSH
  (`docker compose pull && docker compose up -d`). Production is a second VM with the same
  compose file.
- Terraform (Phase 7) describes the VM, firewall, bucket, KMS key and service account so the
  environment is reproducible.
- Migration path, written now so it stays cheap: `api` and `worker` are stateless containers
  and move to Cloud Run unchanged; `db` moves to Cloud SQL (Postgres 17) by restoring a base
  backup plus WAL; nginx is replaced by a load balancer. Trigger: first paying customers or a
  measured need for more than one VM.

## Consequences

- Lowest cost and the shortest path to a running pilot using patterns the team maintains.
- A single VM is a single point of failure. Mitigated by backups with PITR, health checks,
  `restart: unless-stopped`, and alerting; accepted for the pilot stage, documented in the SLA.
- Operating Postgres ourselves means owning upgrades and vacuum tuning until the Cloud SQL move.
