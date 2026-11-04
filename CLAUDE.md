# OpenShelf

A self-hosted media tracker built as a single Go binary on Postgres. Read `docs/plan.md` (tasks and working agreement), `docs/data-model.md` (schema contract), and `docs/decisions/` (ADRs) before starting a task.

## Commands

Tools are pinned in `mise.toml`; the Makefile puts them on `PATH`.

```
make db-up        # Postgres 18 on localhost:5433 (compose.dev.yaml)
make dev          # templ + tailwind watch, restarts the server on change
make generate     # sqlc + templ + tailwind (run after editing .sql, .templ, or classes)
make check        # generate-diff, vet, golangci-lint, tests (must pass before you finish)
make test         # go test ./...
go run ./cmd/openshelf user create --admin --username alice --password password123
```

Tests need the dev Postgres running (`make db-up`). Each test gets a fresh database cloned from a migrated template.

## Layout

```
cmd/openshelf/          CLI: serve, worker, migrate, seed, user create
internal/app/           wiring: pool, sessions, River client, worker registry
internal/server/        middleware chain and route registration (routes.go)
internal/deps/          Deps bundle passed to every feature package
internal/auth/          passwords, session login, CurrentUser, RequireUser/RequireAdmin
internal/login/         /login and /logout
internal/db/            sqlc output (generated) + queries/<package>.sql (hand-written)
internal/migrations/    goose SQL migrations, embedded
internal/web/           Render/Redirect/Error/Flash/PageFor helpers
internal/web/ui/        layout and shared templ components
internal/web/form/      form parsing and validation
internal/web/static/    embedded assets; app.css is generated
internal/testutil/      NewDB, NewServer, LoginAs, CreateUser, CountQueries
```

## Conventions

- **Handlers:** each feature package exposes `Register(mux *http.ServeMux, d *deps.Deps)`. Use Go 1.22+ patterns (`"POST /items/{id}/edit"`). Wrap with `auth.RequireUser` / `auth.RequireAdmin`.
- **Rendering:** `web.Render(w, r, status, component)`. Full pages wrap content in `ui.Layout(web.PageFor(r, d, title, navKey))`. For partial htmx requests (`web.IsPartial(r)`), render only the fragment. After a successful POST, use `web.Redirect` (303, or `HX-Redirect` for partials).
- **Errors:** `web.Error(w, r, d, err)` maps `pgx.ErrNoRows` to 404 and logs everything else as a 500. A missing row and a row the viewer can't see both return 404.
- **Forms:** `f, err := form.Parse(w, r)`, then `f.Required(...)`, `f.MaxLen(...)`, and so on. Re-render with status 422 and `f.Errors` on failure. Put form-level errors in `f.Errors["_form"]`.
- **SQL:** queries live in `internal/db/queries/<package>.sql`, and query names start with the package name (`LibraryListEntries`). Always filter user-owned rows by the viewer **in SQL** (ADR 0004). Use `d.WithTx` for multi-statement writes.
- **Migrations:** forward-only goose files `NNNNN_name.sql` with `-- +goose Up`. Use only your task's number range (docs/plan.md). Never edit a merged migration.
- **Jobs:** River workers are registered in `internal/app/workers.go` via each package's `Workers(...)`. Enqueue inside the same transaction as the data change when possible (`d.River.InsertTx`).
- **Generated files** (`internal/db/*.go`, `*_templ.go`, `internal/web/static/app.css`) are committed. Never edit or hand-merge them; rerun `make generate`.
- **Styling:** Tailwind utility classes with theme tokens (`bg-surface`, `text-muted`, `border-line`, `bg-accent`). Reuse `internal/web/ui` components rather than restyling.
- **Dependencies:** no CGO, no JS build step, no new runtime services. Justify any new Go module in your report.
- **Code style:** keep comments minimal. Comment only non-obvious intent, never restate the code.
- **Git:** no attribution or co-author lines in commits or PRs. Keep PR descriptions terse.
