# Implementation plan

The plan has a small **serial foundation** (Wave 0), followed by **vertical slices** that run in parallel. Each slice delivers its own routes, queries, templates, jobs, and tests, end to end. The foundation exists so that slices never need to edit each other's files.

Read first: [ADR 0001](decisions/0001-go-monolith.md) (stack), [ADR 0002](decisions/0002-source-and-user-fields.md) (source/user fields), [ADR 0003](decisions/0003-mvp-scope.md) (release scope), [ADR 0004](decisions/0004-identifiers-and-visibility.md) (IDs and visibility), and [data-model.md](data-model.md) (schema contract).

```
Wave 0  F1 ──► F2                                  (serial; one agent)
Wave 1  S01 S02 S03 S04 S05 S06 S07                (batch 1a, parallel)
        S08 S09 S10 S11 S12 S13 S14                (batch 1b, parallel; can run alongside 1a)
        I1  integration + acceptance + polish      (serial)          ──► v0.1
Wave 2  F4 ──► S20 S21 S22                          ──► v0.2 (Steam)
Wave 3  S30 S31 S32 S33  (S32/S33 need F4)          ──► v0.3 (Retro)
Wave 4  S40 S41 S42                                 ──► v0.4 (Sharing)
Wave 5  F5 ──► S50 S51 S52                          ──► v0.5 (Federation)
```

---

## Working agreement for parallel agents

These rules are what make fan-out safe. Every slice brief assumes them.

1. **One branch and worktree per task:** `task/<ID>-<name>`, created from the latest `master`.
2. **Edit only the paths you own.** Each brief lists owned paths. Anything else is read-only. If a shared contract (`internal/deps`, `internal/web/ui`, `internal/activity`, `internal/providers`, `internal/testutil`, `docs/data-model.md`) needs a change, don't make it. List it under **Contract requests** in your final report, and work around it locally if possible.
3. **Migrations use reserved number ranges.** Goose runs with `allowMissing`, so merge order doesn't matter.
   - F1: `00001–00049`, F2: `00050–00099`, F4: `01900–01999`, F5: `04900–04999`
   - Slice `Snn`: `nn00–nn99`, for example S09 uses `00900_…sql`, S20 uses `02000_…sql`
   - Slice migrations may only **add** slice-private tables or indexes. They never alter foundation tables (that's a contract request).
4. **SQL lives in your own query file:** `internal/db/queries/<slice>.sql`, where `<slice>` is the package name (e.g. `library.sql`). Prefix query names with your package (`LibraryListEntries`) to avoid collisions. Shared queries in `core.sql` (owned by F2) are read-only.
5. **Never hand-edit or hand-merge generated files.** After merging, run `make generate` and commit the result.
6. **Every read of user-owned data filters by viewer in SQL** (ADR 0004). Every slice includes a test proving user B can't see or modify user A's private rows through any of its routes.
7. **Definition of done:** `make check` passes (generate-diff, `go vet`, lint, all tests). Every acceptance criterion in the brief has a test. The page works when you run `make dev` against `openshelf seed` data. The final report lists: what shipped, owned files touched, contract requests, and known follow-ups.
8. **No new dependencies** beyond ADR 0001 without noting the reason in your report. No CGO. No JavaScript build step.

### Standard package shape (every feature slice)

```
internal/<pkg>/
  handlers.go        # Register(mux *http.ServeMux, d *deps.Deps); HTTP handlers
  service.go         # Domain logic; takes *db.Queries or pgx.Tx; no HTTP types
  views.templ        # Pages and partials; use internal/web/ui components
  panel.go           # Optional: ItemPanel(...) for the item detail page
  workers.go         # Optional: Workers(w *river.Workers, d *deps.Deps)
  *_test.go          # Service tests + HTTP tests via testutil.NewServer
internal/db/queries/<pkg>.sql
internal/migrations/<range>_<name>.sql   # only if the slice needs private tables
```

---

## Wave 0 — Foundation (serial)

### F1 — Walking skeleton, tooling, auth core

**Goal:** `make dev` starts a server that logs you in and shows an empty app shell. CI is green. An agent can add a feature by following `CLAUDE.md`.

**Delivers**
- `mise.toml` pinning Go (latest stable), sqlc, golangci-lint, and the Tailwind standalone CLI. `go.mod` `tool` directives for templ.
- `Makefile`: `dev` (templ watch + tailwind watch + `go run`, using `air` or a simple watcher script), `generate` (templ, sqlc, tailwind), `check` (generate + `git diff --exit-code` + vet + lint + test), `test`, `db-up`, `db-reset`, `seed`, `build`, `image`.
- `compose.dev.yaml`: Postgres 18 on port 5433 with a named volume.
- `cmd/openshelf/main.go` with subcommands `serve`, `worker`, `migrate`, `seed` (stub), `user create`.
- `internal/config`: env parsing (`OPENSHELF_DATABASE_URL`, `OPENSHELF_LISTEN`, `OPENSHELF_BASE_URL`, `OPENSHELF_DATA_DIR`, `OPENSHELF_SECRET_KEY`, `OPENSHELF_ENV`, `OPENSHELF_AUTO_MIGRATE`, provider keys `OPENSHELF_TMDB_TOKEN`, `OPENSHELF_IGDB_CLIENT_ID`, `OPENSHELF_IGDB_CLIENT_SECRET`, `OPENSHELF_STEAM_API_KEY`).
- `internal/deps`: the `Deps` struct (`Config`, `Pool *pgxpool.Pool`, `Q *db.Queries`, `River *river.Client[pgx.Tx]`, `Sessions *scs.SessionManager`, `Log *slog.Logger`, `Now func() time.Time`) and a `WithTx(ctx, fn func(q *db.Queries, tx pgx.Tx) error)` helper.
- `internal/server`: mux, middleware chain (request ID, slog access log, panic recovery, security headers + CSP, `http.CrossOriginProtection`, sessions `LoadAndSave`, current-user loader), `/healthz` (process up), `/readyz` (DB ping), `/static/*` with immutable caching from `embed.FS`.
- `internal/migrations`: embedded goose with `allowMissing`, plus River's migrations, run by `migrate` and optionally on startup. Migration `00001_accounts.sql` creates `users` and `sessions` as defined in data-model.md.
- `internal/db`: `sqlc.yaml` (engine postgresql, schema = `internal/migrations`, queries = `internal/db/queries`, pgx/v5, emit enums with valid checks), `queries/core.sql` with user queries.
- `internal/auth`: argon2id hash/verify, `CurrentUser(ctx) (db.User, bool)`, `RequireUser` / `RequireAdmin` middleware, `LogIn`/`LogOut`. `internal/login`: login/logout handlers + form with a per-IP rate limit. CLI `user create [--admin]`.
- `internal/web/ui`: layout (sidebar nav matching the mockups: Home, Library, Journal, Lists, Stats, Discover; Libraries: Games, Books, Movies, TV; user menu), flash messages, and components: `Button`, `LinkButton`, `Field` (label, input, error), `Select`, `Textarea`, `Card`, `CoverCard` (cover, title, kind icon, status chip), `CoverGrid`, `Table`, `Badge`, `Tabs`, `EmptyState`, `Pagination` (keyset cursor), `Modal` (htmx target), `Icon` (inline SVG set for kinds and actions). Tailwind theme tokens use the mockups' dark violet palette, with a light theme via `prefers-color-scheme`. htmx is vendored at `internal/web/static/htmx.min.js`.
- `internal/web/form`: tiny helpers to decode, validate, and re-render forms with errors (`form.Errors`, `form.Values`), used by every slice.
- `internal/testutil`: `NewDB(t)` (creates a migrated template DB once per `go test` run and clones it per test with `CREATE DATABASE … TEMPLATE`), `NewServer(t)` (full app over `httptest`), `Client` with cookie jar plus `Login(t, user)`, `CreateUser(t, …)`, and an htmx request helper.
- `Dockerfile` (multi-stage to distroless static), `.github/workflows/ci.yml` (Postgres service, `make check`), `.golangci.yml`, `.editorconfig`.
- `CLAUDE.md`: commands, layout, working agreement summary, how to add a route, query, template, migration, or job, and links to the ADRs.

**Acceptance**
- `make db-up && make dev`, then `openshelf user create --admin alice` lets you log in at `/login` and see the shell.
- `make check` is green locally and in CI. A test logs in through HTTP and gets a 200 on `/`.
- `docker build` produces an image under 40 MB. Idle RSS under 60 MB (record the measurement in the PR).

### F2 — Domain contract, stubs, seed data

**Goal:** All v0.1 tables, shared contracts, and stub packages exist, so every Wave 1 slice has its schema, its package, a place on the item page, and realistic data.

**Delivers**
- Migrations `00050_catalog.sql`, `00051_personal.sql`, `00052_platforms_seed.sql`, implementing data-model.md exactly.
- `queries/core.sql`: shared reads every slice needs: `GetMediaItemByPublicID`, `GetMediaItemsByIDs`, `ListEditionsForItem`, `ListCreditsForItems`, `GetPlatform*`, `ListPlatforms`, `CatalogSearch(query, kind, limit)` (tsvector + trigram ranking).
- `internal/activity`: `Record(ctx, q, activity.Event{UserID, Verb, MediaItemID, LibraryEntryID, ExperienceID, ReviewID, ListID, Visibility, OccurredAt})`. This is the only way to write `activity_events`.
- `internal/images`: contract functions `Enqueue(ctx, q, sourceURL) (imageID int64, err)` (inserts a `pending` row and returns the existing ID if the URL is already known) and `CoverURL(imageID *int64, kind db.MediaKind, size images.Size) string` (returns `/covers/{id}/{size}` when ready, else an embedded per-kind placeholder SVG). S13 implements fetching and serving.
- `internal/providers`: the metadata adapter contract.
  ```go
  type Provider interface {
      Name() string                       // "openlibrary", "tmdb", "igdb"
      Kinds() []db.MediaKind
      Configured() bool                   // false if required keys are missing
      Search(ctx context.Context, kind db.MediaKind, query string) ([]SearchResult, error)
      Fetch(ctx context.Context, kind db.MediaKind, externalID string) (*Item, error)
  }
  func Register(p Provider); func For(kind db.MediaKind) []Provider; func Get(name string) (Provider, bool)
  ```
  The normalized types (`SearchResult`, `Item`, `Edition`, `Credit`, `ExternalID`, `Details` per kind) mirror data-model.md columns. Also `providertest.Conformance(t, p, fixtures)`: a shared contract test every adapter runs against recorded HTTP fixtures (`httptest` server + `testdata/*.json`). Plus a shared HTTP client with timeouts, a user agent, and per-provider rate limiting (`golang.org/x/time/rate`).
- Stub packages with no-op `Register` / `Workers` / `ItemPanel`, all wired into `internal/server/routes.go` and the worker registry once, so slices never touch wiring: `catalog`, `library`, `experiences`, `reviews`, `lists`, `journal`, `dashboard`, `stats`, `accounts`, `exchange` (import/export), `discover`, `providers/openlibrary`, `providers/tmdb`, `providers/igdb`, `images`, `pwa`.
- Item detail page shell at `GET /items/{public_id}` (owned by F2, later by S01 for the header only). It renders the catalog header, then panels in fixed order: `library.ItemPanel`, `experiences.ItemPanel`, `reviews.ItemPanel`, `lists.ItemPanel`. Each has the signature `func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error)`.
- `testutil` factories: `CreateItem(t, kind, opts)`, `CreateEdition`, `CreateEntry`, `CreateExperience`, `CreateReview`, `CreateList`.
- `openshelf seed`: idempotent demo data. Users `alice` (admin) and `bob` with password `password` (dev only; refuses to run when `OPENSHELF_ENV=production`). About 40 items across the four kinds with editions and platforms (including a Dreamcast and a PS2 release of the same game). Entries in every ownership/backlog state, active/finished/abandoned experiences, reviews with all visibilities, two mixed lists, and backfilled activity events.

**Acceptance**
- Migrations apply cleanly to an empty DB and `sqlc generate` succeeds. A test exercises each enum and constraint (e.g. the `external_ids` single-subject check, `nulls not distinct`).
- `/items/{id}` renders a seeded item with empty panels. Every stub route compiles and is wired.
- `providertest.Conformance` passes against a fake in-memory provider.

---

## Wave 1 — v0.1 vertical slices (parallel)

Each brief lists **Owns** (exclusive paths and routes), **Uses** (read-only contracts), **Acceptance** (each one needs a test), and **Out of scope**. All slices depend only on F2.

### Batch 1a

#### S01 — Catalog: manual entry and item pages
- **Owns:** `internal/catalog/**`, `queries/catalog.sql`, migrations `001xx`; routes `GET /items/new`, `POST /items`, `GET|POST /items/{id}/edit`, `/items/{id}/editions/*`, `GET /search`; the item page header component.
- **Delivers:** kind-aware create/edit forms (common fields + the kind's details table), creator/credit editing (find or create a creator by name), editions CRUD (format, platform picker with alias search, region, page count, duration), `sort_title` computation, `locked_fields` set automatically when an admin edits a provider-sourced field, and a global search box (`/search?q=`, htmx live results from `CatalogSearch`, grouped by kind). The item header shows the cover through `images.CoverURL` and links to `/items/{id}/cover` (implemented by S13).
- **Acceptance:** creating a book, game, film, or show manually works end to end. A game gets a Dreamcast edition and a PS2 edition under one item. "Mega Drive" finds Genesis in the platform picker. Members can create items; only admins can edit items they didn't create. Editing a provider field adds it to `locked_fields`. Search ranks an exact title above a partial one.
- **Out of scope:** provider import (S10), covers (S13), merging duplicates (later).

#### S02 — Library
- **Owns:** `internal/library/**`, `queries/library.sql`, migrations `002xx`; routes `GET /library`, `GET /library/{kind}`, `POST /items/{id}/library`, `POST|DELETE /library/{entry_id}`, `POST /library/{entry_id}/played`; `library.ItemPanel`.
- **Delivers:** add/remove an entry (optionally per edition), set ownership/backlog/acquired date/tags/notes/visibility, explicit "Played / Not played / Reset" (writes `played_user` only), and the library page. The library page has a cover grid and a compact table view (toggle remembered in the session), kind tabs, filters (ownership, derived status, platform, format, genre, tag, rating, date added), sort (title, added, release, rating), and keyset pagination. Filter state lives in the query string so it's shareable and bookmarkable.
- **Acceptance:** adding the same item twice without an edition is rejected, but PC and Switch editions are separate entries. Status filters match the derived definitions in data-model.md. Grid rendering of 500 entries uses ≤ 5 queries (assert with a pgx query-count tracer in `testutil`). Each add records an `added` activity event. Cross-user isolation test.
- **Out of scope:** source columns and sync badges (v0.2).

#### S03 — Experiences and progress
- **Owns:** `internal/experiences/**`, `queries/experiences.sql`, migrations `003xx`; routes `POST /items/{id}/experiences`, `POST /experiences/{id}/{start|pause|resume|finish|abandon}`, `POST /experiences/{id}/progress`, `GET|POST /experiences/{id}/edit`, `DELETE /experiences/{id}`; `experiences.ItemPanel`; quick-log partial `experiences.QuickLog(item)` (for S06 to embed).
- **Delivers:** a state machine with explicit allowed transitions. "Revisit" creates a new experience. The progress unit defaults by kind (book→pages with total from the edition's `page_count`, show→episodes, game→hours or percent, film→none). Progress updates append `progress_events` and move `progress_current`. Dates are editable, including backdating, and support finished-without-start. Each transition records an activity event. htmx in-place updates on the item page.
- **Acceptance:** an invalid transition (finish→pause) gets a 422. Progress past the total is clamped or rejected. Two finished experiences of one film show as "watched twice". Abandoning sets `finished_on`. Cross-user isolation test.

#### S04 — Ratings and reviews
- **Owns:** `internal/reviews/**`, `queries/reviews.sql`, migrations `004xx`; routes `GET|POST /items/{id}/review`, `DELETE /reviews/{id}`, `GET /reviews/{id}`; `reviews.ItemPanel`; component `reviews.RatingInput(display, value)` and `reviews.RatingDisplay(display, value)` (exported for S02, S06, and S07 to use).
- **Delivers:** a rating widget for each `rating_display` (half-star 5-star built from radio inputs, 10-point select, 0–100 number input), all stored as 0–100. A review body (plain text with paragraphs; no HTML or markdown in v0.1, so there's no XSS surface), a spoiler flag (collapsed by default when viewed), visibility, and an optional link to an experience. Records `rated` / `reviewed` activity.
- **Acceptance:** a 4.5-star rating stores 90 and renders as 9/10 and 90/100 for other display preferences. A spoiler review renders collapsed. An `instance` review is visible to another logged-in user at `/reviews/{id}`, while a `private` one returns 404 (not 403) to them.

#### S05 — Lists
- **Owns:** `internal/lists/**`, `queries/lists.sql`, migrations `005xx`; routes `/lists`, `/lists/new`, `/lists/{id}`, `/lists/{id}/edit`, `POST /lists/{id}/items`, `POST /lists/{id}/items/{item}/move`, `DELETE /lists/{id}/items/{item}`; `lists.ItemPanel` ("Add to list" dropdown).
- **Delivers:** CRUD for lists with name, description, ranked flag, and visibility. Mixed-media items; ranked lists show numbers. Reordering via up/down/top buttons (htmx; positions renumbered in a transaction). A list page with a cover grid, a list index with cover collage thumbnails (first 4 covers). Records a `listed` activity.
- **Acceptance:** a list holds a game, a book, and a film. Moving an item keeps positions contiguous. Adding a duplicate is a no-op with a flash message. Cross-user isolation test, plus an `instance` list readable but not editable by another user.

#### S06 — Home dashboard and journal
- **Owns:** `internal/dashboard/**`, `internal/journal/**`, `queries/dashboard.sql`, `queries/journal.sql`, migrations `006xx`; routes `GET /` and `GET /journal`.
- **Delivers:** a home page matching the dashboard mockup: greeting, counters (cataloged / in progress / backlog / completed), a "Currently enjoying" cover row with inline quick-log (embeds `experiences.QuickLog`), "Recently finished", a recent activity feed, and "From your backlog" (three random backlog items, stable per day). A journal page with a timeline grouped by day in the user's timezone, filters by kind and verb, keyset pagination, and rendering for every `activity_verb`.
- **Acceptance:** counters match the derived definitions on seed data. The journal groups days correctly across a DST boundary in `America/New_York`. The page renders when an event's source row was deleted (cascade means it's gone; no 500s).

#### S07 — Stats
- **Owns:** `internal/stats/**`, `queries/stats.sql`, migrations `007xx`; route `GET /stats`.
- **Delivers:** a period selector (this year, last year, all time, custom year), counts of completed items by kind, completions per month, average rating and rating distribution, pages read, hours from experiences where a unit allows it, and top genres. Charts are server-rendered inline SVG (donut and bar); no JS chart library. Each chart has an accessible data table fallback.
- **Acceptance:** numbers on fixed fixture data match hand-computed expected values. Empty states render with no data. One query per widget at most.

### Batch 1b

#### S08 — Accounts, settings, admin
- **Owns:** `internal/accounts/**`, `queries/accounts.sql`, migrations `008xx`; routes `GET|POST /setup`, `/settings`, `/settings/profile`, `/settings/password`, `/admin/users/*`.
- **Delivers:** a first-run setup wizard (only when there are zero users: create the admin, then show which providers are configured from env). Settings for display name, email, password change, default visibility, rating display, and timezone (picker from `time/tzdata`). Admin pages to list users, create users with a temporary password, disable/enable, reset password, and promote/demote. The last admin can't be demoted or disabled. Disabled users' sessions are invalidated.
- **Acceptance:** `/setup` 404s once any user exists. A disabled user is logged out on their next request. A member gets 403 on `/admin/*`. Changing the password requires the current password.

#### S09 — Export and import
- **Owns:** `internal/exchange/**`, `queries/exchange.sql`, migrations `009xx` (`import_jobs` table), `docs/export-format.md`, `docs/import-csv.md`; routes `/settings/export`, `/settings/import/*`.
- **Delivers:** a JSON export (streamed, versioned `"format": "openshelf.export.v1"`) of everything the user owns: entries, experiences, progress events, reviews, lists, and activity, with referenced catalog items embedded (title, kind, year, external IDs) so the export is self-describing. A CSV import with a documented column set (`kind,title,year,isbn13,tmdb_id,igdb_id,platform,ownership,backlog,status,started_on,finished_on,rating_100,review,tags`). Import runs as a River job, matches by external ID first, then exact title+year+kind, and otherwise creates a manual item. It has a dry-run preview page showing create/match/skip counts and is idempotent: re-importing the same file creates nothing new.
- **Acceptance:** export → wipe user → import the JSON export round-trips (re-importing JSON is part of this slice). Importing the same CSV twice is idempotent. Bad rows are reported with line numbers and don't abort the job. Another user's data never appears in an export.

#### S10 — Discover and Open Library
- **Owns:** `internal/discover/**`, `internal/providers/openlibrary/**`, `queries/discover.sql`, migrations `010xx`; routes `GET /discover`, `POST /discover/import`, `POST /items/{id}/refresh`, `GET /admin/providers`.
- **Delivers:** a Discover page that searches the local catalog first (`CatalogSearch`), then configured providers for the selected kind, concurrently with per-provider timeouts. Results show "already in catalog". Imports deduplicate by `external_ids`. The importer turns a normalized `providers.Item` into a media item, details, editions, credits, and external IDs in one transaction. It respects `locked_fields` on refresh and calls `images.Enqueue` for covers. A periodic River job refreshes stale metadata (older than 30 days, batches of 50). The Open Library adapter covers work search, work fetch, and editions with ISBNs; it requires no key and sends a contact user agent per their policy. An admin provider status page shows each provider, whether it's configured, and its last error.
- **Acceptance:** importing the same work twice yields one item. Refresh doesn't overwrite a locked title. When a provider times out, the page still shows local results and other providers' results with a notice. The Open Library adapter passes `providertest.Conformance` with recorded fixtures (no network in tests).

#### S11 — TMDB adapter (film and TV)
- **Owns:** `internal/providers/tmdb/**` only.
- **Delivers:** a `Provider` implementation for `film` and `show` using a TMDB v4 read access token (bearer). Search, movie details (runtime, release date, genres, directors/writers, IMDb ID), and TV details (network, status, season/episode counts, creators). Poster URLs come from the configuration endpoint (cached). Attribution text is exposed for the UI.
- **Acceptance:** passes `providertest.Conformance` on fixtures. `Configured()` is false without a token. Retries with backoff on 429 and honors `Retry-After`.

#### S12 — IGDB adapter (games)
- **Owns:** `internal/providers/igdb/**` only.
- **Delivers:** a `Provider` for `game`. Twitch client-credentials token fetch and caching with expiry. Apicalypse queries for search and fetch (summary, first release date, genres, involved companies → developer/publisher credits, release dates per platform → editions). IGDB platform IDs map to local `platforms` via `external_ids` (`igdb_platform`), falling back to an alias match. Unknown platforms are reported in the result rather than invented. Keeps a 4 req/s rate limit.
- **Acceptance:** passes conformance on fixtures. A game released on Dreamcast and PS2 yields two editions mapped to the seeded platforms. The token refreshes when expired.

#### S13 — Cover images
- **Owns:** `internal/images/**` (implementing F2's contract without changing signatures), `internal/safehttp/**`, migrations `013xx`; routes `GET /covers/{id}/{size}`, `GET|POST /items/{id}/cover` (upload/replace page linked from the item header).
- **Delivers:** `safehttp`, an outbound client for arbitrary URLs. It allows only http/https, resolves DNS and blocks private, loopback, link-local, and metadata addresses (dialer `Control` hook), limits redirects and response size (10 MB), and enforces timeouts. A River `FetchImage` job downloads, decodes (jpeg/png/gif/webp), resizes to `thumb` 200w, `card` 400w, and `full` 800w JPEG, and stores files content-addressed under `$OPENSHELF_DATA_DIR/covers/ab/abcdef…-card.jpg`. Serves with `Cache-Control: public, max-age=31536000, immutable`. Uploads go through the same pipeline. Per-kind placeholder SVGs.
- **Acceptance:** a URL resolving to `127.0.0.1` or `169.254.169.254` is refused. Oversized and non-image responses mark the image `failed` without crashing the worker. Identical bytes from two URLs share one file. Resizing a 4000px image stays under 50 MB peak memory (decode with size limits).

#### S14 — Ops, deploy, and PWA
- **Owns:** `deploy/**`, `internal/pwa/**`, `docs/self-hosting.md`, `docs/backup-restore.md`, `scripts/**`, `.github/workflows/release.yml`; routes `/manifest.webmanifest`, `/sw.js`, `/offline`.
- **Delivers:** a production `deploy/compose.yaml` (app + Postgres 18, healthchecks, named volumes for `pgdata` and `data`, env file template). Backup and restore docs using `pg_dump` in the Postgres container plus the data volume, with a tested restore script. A release workflow building multi-arch images (amd64 + arm64) to GHCR on tag. `scripts/resource-budget.sh` starts the stack, seeds it, waits 60 s idle, and asserts RSS < 60 MB and CPU < 1%, run in CI. A PWA manifest, icons, and a minimal service worker (caches static assets and the offline page only; no data caching).
- **Acceptance:** fresh clone → `cp .env.example .env && docker compose up` gives a working setup wizard. The backup/restore script round-trip is verified in CI against seeded data. The resource-budget script passes.

### I1 — Integration, acceptance, polish (serial, after Wave 1 merges)
- Merge all slices, run `make generate`, and resolve contract requests.
- Write `internal/acceptance/mvp_test.go` covering the RFC's MVP acceptance test over HTTP. A new instance → setup → add a game, book, and film (manual + fixture-backed provider) → mark one owned but untouched → finish another → rate and review → put all three in a list → export → back up and restore → verify.
- A UI consistency pass against the mockups, a keyboard/screen-reader pass (labels, focus states, skip link), and a mobile-width pass.
- Check performance budgets and N+1 queries on the library, home, and journal pages with 2,000 seeded entries.
- Tag **v0.1.0**.

---

## Wave 2 — v0.2 Steam

### F4 — Sync framework (serial)
`internal/sync`, migrations `019xx`. Delivers:
- `connected_accounts` (user, provider, external account ID, display name, encrypted credentials, scopes, status, cursor, last sync).
- `internal/secrets` (AES-256-GCM using a key derived from `OPENSHELF_SECRET_KEY`, with key rotation support).
- `sync_runs` and `sync_events` (before/after values, `respected_override`).
- `import_exclusions` and `external_playtime`.
- Achievement tables: `achievements`, `achievement_unlocks`, `achievement_awards`, keyed by connected account + provider game ID + achievement ID.
- An `AccountAdapter` interface (`Capabilities()`, `Link`, `Sync(ctx, account, cursor) (iterator of observations)`, `Unlink`) and a generic sync runner. It's idempotent and resumable, never deletes on disappearance, writes only `*_source` columns (with a test that enforces this), records sync events, and creates imports as private.
- A River periodic scheduler and a settings page at `/settings/connections` with stub cards per adapter.

### S20 — Steam linking and library import
OpenID 2.0 assertion verification (signature check via `check_authentication`), binding the SteamID to the logged-in user, multiple accounts per user. Owned games import via `IPlayerService/GetOwnedGames` → `steam_app` external IDs → match to catalog (IGDB cross-reference where available, else create a manual item flagged for review). Sets `ownership_source` and `played_source` (from playtime > 0) and stores playtime and last played. Private-profile states are surfaced honestly.

### S21 — Steam achievements
Schema definitions (`GetSchemaForGame`) and player achievements (`GetPlayerAchievements`) for owned games with incremental refresh. Achievement panel on the item page; counts on library cards.

### S22 — Override, history, and exclusion UI
Source badges ("inferred from Steam") on library and item pages, the *Reset to source* action, a sync history screen per connection (runs and events, "override respected" highlighted), per-item "exclude from sync", and an unlink flow with a retain/remove choice. **Acceptance:** the RFC's Portal 2 test, end to end.

## Wave 3 — v0.3 Retro

- **S30 Platform registry:** admin CRUD for platforms and aliases, a "flag a metadata mistake" queue, and an expanded seed list. Local corrections survive IGDB refresh.
- **S31 Platform browsing:** `/platforms/{slug}` catalog browsing independent of the library, filterable by region, year, genre, and ownership/played state. An admin-triggered "fetch platform catalog from IGDB" job. A coverage-incomplete notice.
- **S32 RetroAchievements:** API-key linking (encrypted, via F4), game/system mapping through `external_ids`, unlocks with hardcore/softcore distinction, and awards and mastery exactly as reported by the API.
- **S33 Match review queue:** a `match_candidates` table and UI shared by Steam and RetroAchievements for ambiguous matches, with confirm, choose another, or create manual.
- **Acceptance:** RFC retro criteria 1–7.

## Wave 4 — v0.4 Sharing (single instance)

- **S40 Profiles:** `/@username` with `public` and `instance` visibility, favorites, a stats summary, and recent public activity. Visibility enforcement tests across every public-facing query.
- **S41 Sharing:** shareable list and review pages for logged-out visitors when public, OpenGraph tags, and per-item visibility bulk edit.
- **S42 Local follows:** follow and request/accept within an instance, plus a friends feed on the home page.

## Wave 5 — v0.5 Federation

- **F5 ActivityPub core:** actors for local users, WebFinger, HTTP Signatures (sign + verify), inbox/outbox endpoints, delivery queue with retries (River), remote actor cache, `safehttp` for all fetches, and domain blocks.
- **S50 Remote follows:** Follow, Accept, Reject, and Undo, with a remote profile view.
- **S51 Outbound activity:** publish explicitly public reviews, lists, and finishes as `Create`/`Note`-compatible objects with OpenShelf extensions.
- **S52 Inbound and moderation:** render remote activity in the friends feed, block and mute, and report.
- **Acceptance:** the RFC's two-instance test using a `compose.federation.yaml` with two app instances behind Caddy with local TLS.

---

## Orchestration notes

- **Batch sizing:** Wave 1 runs as two batches of seven agents (1a, then 1b, or both at once if review bandwidth allows). Each agent runs in its own git worktree.
- **Merge order** within a batch doesn't matter, thanks to migration ranges and per-slice query files. After each merge: `make generate && make check`.
- **Contract requests** collected from reports are batched into one small foundation change between waves, never mid-wave.
- **Agent brief template:** "You are implementing task `<ID>` from `docs/plan.md`. Read `CLAUDE.md`, the ADRs, `docs/data-model.md`, and your task section. Follow the working agreement. Stop when the definition of done is met and report as specified."
