[![License: CC BY-NC-SA 4.0](https://img.shields.io/badge/License-CC%20BY--NC--SA%204.0-lightgrey.svg)](https://creativecommons.org/licenses/by-nc-sa/4.0/)

# grbpwr-manager

Backend service for the [grbpwr.com](https://grbpwr.com) store. It manages products, media, orders,
payments, customers and analytics, and serves the public storefront, the admin panel, and customer auth
from a single Go process.

## Overview

One process exposes **gRPC + a JSON/REST gateway + Swagger UI** on port `:8081`, backed by MySQL and a set
of background workers.

- **Language:** Go 1.26 · module `github.com/jekabolt/grbpwr-manager`
- **APIs:** three Protobuf services — `admin`, `frontend` (storefront), `auth` — compiled with [buf](https://buf.build)
- **Database:** MySQL (`sqlx`), with numbered SQL migrations
- **Media storage:** DigitalOcean Spaces (S3-compatible)
- **Payments:** Stripe (live + test card processors)
- **Email:** Resend (transactional, with bounce/complaint + unsubscribe webhooks)
- **Analytics:** GA4 (reporting), GA4 Measurement Protocol (server events), BigQuery
- **Hosting:** DigitalOcean App Platform (Docker)

## Quickstart

Prerequisites: **Go 1.26**, [`buf`](https://buf.build/docs/installation), Docker (optional), and a `.env`
file with secrets (DB DSN, Stripe, S3, Resend, JWT secrets — see [Configuration](#configuration)).

```shell
# one-time: install codegen tooling (protoc plugins, mockery, oapi-codegen)
make install

# build everything (proto + codegen) and run with .env loaded
make run

# faster iteration (skips mock generation)
make run-quick
```

The service listens on `:8081`. Open `http://localhost:8081/` for the Swagger UI, `GET /readyz` and
`GET /livez` for health.

### Docker

```shell
make image      # build image grbpwr/grbpwr-pm:master
make image-run  # run it on :8081 with ./config mounted
```

## Configuration

Configuration is loaded by `config/cfg.go` using [viper](https://github.com/spf13/viper): an optional TOML
file overlaid by **environment variables, which take precedence**.

- File: `./config/config.toml` by default, or pass `-c/--config <path>`.
- Env vars: each setting is bound explicitly (e.g. `MYSQL_DSN`, `AUTH_JWT_SECRET`, `BUCKET_S3_ACCESS_KEY`,
  `STRIPE_PAYMENT_SECRET_KEY`, `MAILER_SENDGRID_API_KEY`). For local dev these live in `.env` (gitignored)
  and are sourced by `make run`.
- The full set of variables is documented by the production app spec, `.do/app.yaml`.

Key groups: `mysql.*`, `http.*`, `auth.*` (admin JWT), `storefront_auth.*` (customer JWT / magic link),
`bucket.*` (Spaces), `mailer.*` (Resend), `stripe_payment.*` / `stripe_payment_test.*`, `revalidation.*`
(Vercel ISR), `ga4.*` / `ga4mp.*` / `bigquery.*`, and worker intervals.

## Project layout

```
cmd/                     entrypoint (cobra: run + version)
app/                     App.Start wires store, workers and the HTTP server
config/                  viper config loader + TOML; config/certs/ holds the DB CA cert
proto/                   .proto contracts (admin, auth, frontend, common) — compiled with buf
  gen/                   generated Go (do not edit)
internal/
  api/http/              gRPC + REST gateway + Swagger UI + health/webhooks
  apisrv/{admin,frontend,auth}   gRPC service implementations
  store/                 MySQL repository; store/sql/ holds migrations
  dependency/            interfaces + generated mocks
  dto/ , entity/         transport vs domain models
  mail/ bucket/ payment/ analytics/ ...   integrations & domain logic
  ordercleanup/ storefrontcleanup/ tiermanagement/ stripereconcile/   background workers
.do/                     DigitalOcean App Platform specs (prod: app.yaml)
Dockerfile , Makefile
```

## Development

- **Protobuf:** edit files under `proto/**`, then `make proto` to regenerate. Never edit `*.pb.go` /
  `*.gw.go` by hand.
- **Mocks:** interfaces in `internal/dependency`; run `make generate` (mockery) to refresh mocks.
- **Migrations:** add the next-numbered file in `internal/store/sql/` (e.g. `0059_xxx.sql`); never edit an
  already-applied migration. They auto-apply on boot when `MYSQL_AUTOMIGRATE=true`.
- **Lint / test:** `make lint`, `make cov`.

## Deployment

Deployed on DigitalOcean App Platform from `/Dockerfile`. Two environments share one codebase, differing
only by environment variables:

| Env  | App                   | Branch   | API domain                | Database      |
|------|-----------------------|----------|---------------------------|---------------|
| prod | `grbpwr-backend`      | `master` | `backend.grbpwr.com`      | `grbpwr`      |
| beta | `grbpwr-backend-beta` | `beta`   | `backend-beta.grbpwr.com` | `grbpwr_beta` |

Specs live in `.do/` (`app.yaml` for prod). Workflow: feature → `beta` → verify → merge to `master`.

## License

Licensed under the Creative Commons Attribution-NonCommercial-ShareAlike 4.0 International License — see
[LICENSE](LICENSE).
