# ADR 0001 — Go monolith, server-rendered, Postgres only

- **Status:** Accepted
- **Date:** 2026-11-04
- **Supersedes:** the "Initial stack (proposed)" table in the README RFC (Next.js + NestJS/FastAPI)

## Context

OpenShelf is a self-hosted, multi-user app that homelab users will run next to many other services. Its requirements:

- **Low resource use.** Idle CPU near zero and a small, predictable memory footprint. We don't want another app that needs a dedicated VM.
- **Simple operations.** One container plus PostgreSQL. No Redis, no Node runtime, no separate frontend deployment.
- **Mostly agent-written code.** A coding agent will write most of the code, so the stack should give fast, mechanical feedback (types, compiler, generated query code) and use stable APIs that agents won't invent or mix across versions.
- **No SPA needed.** Server-rendered HTML with partial page updates is enough for cover grids, forms, and quick logging.

Alternatives considered: Rails 8 (best out-of-the-box productivity, ~250 MB idle), Phoenix/Elixir (excellent job and real-time story, but a smaller corpus so agents are less reliable with it), Django (likely ends up needing Celery and Redis), Next.js/Nest (SPA pull, two runtimes).

## Decision

Build **one Go binary** that serves HTTP, runs background jobs in-process, embeds its migrations and static assets, and talks to **PostgreSQL 18**.

| Concern | Choice | Notes |
| --- | --- | --- |
| Language | Go (latest stable, pinned in `mise.toml`) | `go tool` directives in `go.mod` for dev tools where practical |
| HTTP | stdlib `net/http` `ServeMux` (method + wildcard patterns) | No web framework |
| CSRF | stdlib `http.CrossOriginProtection` | Plus `SameSite=Lax` session cookies |
| HTML | [templ](https://templ.guide) | Type-checked components; generated `_templ.go` files are committed |
| Interactivity | [htmx](https://htmx.org), vendored and embedded | Alpine.js only if a widget truly needs client state |
| CSS | Tailwind CSS v4 **standalone CLI** | No Node; built CSS is embedded |
| DB driver | `jackc/pgx/v5` (`pgxpool`) | |
| Queries | [sqlc](https://sqlc.dev) | Hand-written SQL, generated typed Go; generated code is committed |
| Migrations | `pressly/goose/v3`, SQL files embedded with `embed.FS` | Run by `openshelf migrate` and on startup (configurable) |
| Jobs | [River](https://riverqueue.com) (`riverpgxv5`) | Postgres-backed; workers run in the same process by default |
| Sessions | `alexedwards/scs/v2` + `pgxstore` | Sessions stored in Postgres |
| Passwords | `golang.org/x/crypto/argon2` (argon2id) | |
| Images | `golang.org/x/image` (decode + `draw.CatmullRom`), JPEG output | Pure Go, so `CGO_ENABLED=0` static binary |
| Logging | stdlib `log/slog` (JSON in prod, text in dev) | |
| Config & secrets | Environment variables only (`OPENSHELF_*`), parsed with stdlib | No config file or secrets UI. How variables get injected (`.env`, Infisical, external-secrets in Kubernetes, ...) is a deployment detail the app doesn't know about |
| Tests | stdlib `testing`, `httptest`, real Postgres (per-test DB cloned from a migrated template) | No database mocks |
| Lint | `go vet`, `golangci-lint` with a small config | |
| Container | Multi-stage build to `gcr.io/distroless/static` | Target image < 40 MB |

### Process model

```
openshelf serve     # HTTP + River workers + periodic jobs (default)
openshelf serve --no-workers / openshelf worker   # optional split for bigger instances
openshelf migrate   # goose + River migrations, then exit
openshelf seed      # demo data for development
openshelf user create --admin ...   # bootstrap admin from the CLI
```

### Resource budget (CI-checked from v0.1)

- Idle RSS **< 60 MB** for `openshelf serve` with workers enabled.
- Idle CPU **~0%**: no polling loops tighter than River's defaults; periodic syncs are scheduled, not continuous.
- Cold start **< 1 s** (excluding migrations).
- Library grid of 500 items renders in **< 100 ms** server time with **≤ 5 queries**.

## Consequences

**Positive**
- Small static binary and image; easy for self-hosters to run on low-end hardware (Raspberry Pi class).
- The compiler, templ, and sqlc catch most wiring mistakes before tests run, which gives agents tight feedback loops.
- Explicit code with no hidden behavior: a handler, a query, and a template are all you need to read.

**Negative / accepted costs**
- We assemble auth, sessions, form validation, flash messages, and pagination ourselves. Mitigated by doing it once in the foundation (see `docs/plan.md`, Wave 0).
- No generated admin panel; admin screens are hand-built and kept minimal.
- htmx partial updates take more care than SPA state; we accept this in exchange for zero client build tooling.

## Rules that follow from this decision

1. No new runtime services (Redis, Elasticsearch, Node, S3) without a new ADR.
2. No CGO dependencies.
3. Generated code (`*_templ.go`, `internal/db/*.sql.go`, `internal/web/static/app.css`) is committed. CI fails if `make generate` produces a diff. Never hand-edit or hand-merge generated files; regenerate them.
4. Every page must render meaningfully with JavaScript disabled where it's cheap to do so; htmx is an enhancement.
