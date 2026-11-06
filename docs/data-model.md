# Data model (v0.1 + v0.2 contract)

This is the schema that Wave 0 (task **F2**) turns into migrations. Every Wave 1 slice codes against it. **Changing this file after Wave 0 needs an orchestrator decision**, because parallel slices depend on it. Slices may add *slice-private* tables in their own migration range (see `docs/plan.md`).

Conventions (ADRs 0002 and 0004):
- `id bigint generated always as identity primary key`; `public_id uuid not null unique default uuidv7()` on anything that appears in a URL or export.
- `created_at` / `updated_at timestamptz not null default now()`. `updated_at` is set by the application, not by triggers.
- Enums are Postgres enums. Adding a value is an additive migration.
- `on delete cascade` from user-owned rows to `users`; catalog rows are never deleted by users.
- Extensions: `citext`, `pg_trgm`.

## Enums

```sql
create type user_role        as enum ('admin', 'member');
create type visibility       as enum ('private', 'instance', 'public');
create type rating_display   as enum ('stars5', 'ten', 'hundred');   -- storage is always 0–100
create type media_kind       as enum ('game', 'book', 'film', 'show');
create type ownership        as enum ('owned', 'borrowed', 'not_owned');   -- no wishlist in v0.1; backlog is a separate flag
create type experience_state as enum ('active', 'paused', 'finished', 'abandoned');
create type progress_unit    as enum ('percent', 'pages', 'chapters', 'minutes', 'hours', 'episodes');
create type edition_format   as enum (
  'hardcover', 'paperback', 'ebook', 'audiobook',          -- books
  'physical', 'digital',                                   -- games
  'bluray', 'uhd_bluray', 'dvd', 'streaming',              -- film/TV
  'other');
create type platform_kind    as enum ('console', 'handheld', 'computer', 'arcade', 'mobile', 'vr', 'other');
create type credit_role      as enum ('author', 'narrator', 'translator', 'illustrator',
                                      'director', 'writer', 'creator', 'actor',
                                      'developer', 'publisher', 'composer', 'other');
create type activity_verb    as enum ('added', 'started', 'paused', 'resumed', 'progressed',
                                      'finished', 'abandoned', 'rated', 'reviewed', 'listed');
create type image_status     as enum ('pending', 'ready', 'failed');
```

## Accounts (F1)

```sql
create table users (
  id                 bigint generated always as identity primary key,
  public_id          uuid not null unique default uuidv7(),
  username           citext not null unique check (username ~ '^[a-z0-9_]{2,30}$'),
  display_name       text not null,
  email              citext unique,
  password_hash      text not null,
  role               user_role not null default 'member',
  default_visibility visibility not null default 'private',
  rating_display     rating_display not null default 'stars5',
  timezone           text not null default 'UTC',
  disabled_at        timestamptz,
  created_at         timestamptz not null default now(),
  updated_at         timestamptz not null default now()
);

-- scs/pgxstore schema
create table sessions (
  token  text primary key,
  data   bytea not null,
  expiry timestamptz not null
);
create index sessions_expiry_idx on sessions (expiry);
```

## Catalog (shared per instance)

```sql
create table images (
  id           bigint generated always as identity primary key,
  source_url   text unique,               -- null for user uploads
  sha256       text,
  content_type text,
  width        int,
  height       int,
  status       image_status not null default 'pending',
  error        text,
  fetched_at   timestamptz,
  created_at   timestamptz not null default now()
);

create table platforms (
  id           bigint generated always as identity primary key,
  slug         text not null unique,       -- 'dreamcast', 'ps2', 'pc'
  name         text not null,              -- 'Sega Dreamcast'
  short_name   text,                       -- 'Dreamcast'
  manufacturer text,
  kind         platform_kind not null default 'console',
  family       text,                       -- 'sega', 'playstation'
  generation   smallint,
  release_year smallint,
  created_at   timestamptz not null default now(),
  updated_at   timestamptz not null default now()
);
create table platform_aliases (
  platform_id bigint not null references platforms on delete cascade,
  alias       citext not null unique,      -- 'Mega Drive', 'DC'
  primary key (platform_id, alias)
);

create table media_items (
  id                    bigint generated always as identity primary key,
  public_id             uuid not null unique default uuidv7(),
  kind                  media_kind not null,
  title                 text not null,
  sort_title            text not null,                 -- app computes ('The Expanse' -> 'Expanse, The')
  original_title        text,
  release_date          date,
  release_year          smallint,                      -- set even when the full date is unknown
  synopsis              text,
  genres                text[] not null default '{}',  -- lowercase, app-normalized
  cover_image_id        bigint references images on delete set null,
  metadata_provider     text,                          -- e.g. 'tmdb'; null = manual
  metadata_refreshed_at timestamptz,
  locked_fields         text[] not null default '{}',  -- ADR 0002: skipped on provider refresh
  created_by_user_id    bigint references users on delete set null,
  created_at            timestamptz not null default now(),
  updated_at            timestamptz not null default now()
);
-- Full-text search uses this exact expression so the index applies.
create index media_items_search_idx on media_items
  using gin (to_tsvector('simple', title || ' ' || coalesce(original_title, '')));
create index media_items_title_trgm_idx on media_items using gin (title gin_trgm_ops);
create index media_items_kind_sort_idx on media_items (kind, sort_title);
create index media_items_genres_idx on media_items using gin (genres);

-- Typed extension tables: at most one row per item, matching its kind (app-enforced).
create table game_details (
  media_item_id bigint primary key references media_items on delete cascade,
  franchise     text
);
create table book_details (
  media_item_id   bigint primary key references media_items on delete cascade,
  series_name     text,
  series_position numeric(6,2)
);
create table film_details (
  media_item_id   bigint primary key references media_items on delete cascade,
  runtime_minutes int
);
create table show_details (
  media_item_id bigint primary key references media_items on delete cascade,
  network       text,
  status        text,           -- 'returning', 'ended', 'canceled' (provider vocabulary)
  season_count  int,
  episode_count int
);

create table editions (
  id               bigint generated always as identity primary key,
  public_id        uuid not null unique default uuidv7(),
  media_item_id    bigint not null references media_items on delete cascade,
  title            text,                    -- null = same as item
  format           edition_format,
  platform_id      bigint references platforms,  -- games only
  region           text,                    -- 'NTSC-U', 'PAL', 'JP', ...
  publisher        text,
  release_date     date,
  page_count       int,
  duration_minutes int,                     -- audiobooks, film cuts
  cover_image_id   bigint references images on delete set null,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now()
);
create index editions_item_idx on editions (media_item_id);
create index editions_platform_idx on editions (platform_id);

create table creators (
  id         bigint generated always as identity primary key,
  public_id  uuid not null unique default uuidv7(),
  name       text not null,
  sort_name  text not null,
  created_at timestamptz not null default now()
);
create index creators_name_trgm_idx on creators using gin (name gin_trgm_ops);

create table credits (
  media_item_id bigint not null references media_items on delete cascade,
  creator_id    bigint not null references creators on delete cascade,
  role          credit_role not null,
  position      smallint not null default 0,
  primary key (media_item_id, creator_id, role)
);

-- ADR 0003: the single home of provider identifiers.
-- `provider` is namespaced by entity type so (provider, value) is globally unique:
--   openlibrary_work, openlibrary_edition, isbn13, isbn10, tmdb_movie, tmdb_tv,
--   imdb, igdb_game, igdb_platform, steam_app, retroachievements_game, retroachievements_system,
--   audiobookshelf_item
create table external_ids (
  id            bigint generated always as identity primary key,
  provider      text not null,
  value         text not null,
  url           text,
  media_item_id bigint references media_items on delete cascade,
  edition_id    bigint references editions on delete cascade,
  platform_id   bigint references platforms on delete cascade,
  creator_id    bigint references creators on delete cascade,
  last_seen_at  timestamptz not null default now(),
  check (num_nonnulls(media_item_id, edition_id, platform_id, creator_id) = 1),
  unique (provider, value)
);
create index external_ids_item_idx on external_ids (media_item_id);
create index external_ids_edition_idx on external_ids (edition_id);
create index external_ids_platform_idx on external_ids (platform_id);
```

## Personal data (per user)

```sql
create table library_entries (
  id               bigint generated always as identity primary key,
  public_id        uuid not null unique default uuidv7(),
  user_id          bigint not null references users on delete cascade,
  media_item_id    bigint not null references media_items,
  edition_id       bigint references editions,            -- e.g. the Dreamcast release
  ownership_user   ownership,                             -- ADR 0002 pair
  ownership_source ownership,                             -- written by sync only (v0.2)
  backlog          boolean not null default false,        -- "want to play/read/watch"
  played_user      boolean,                               -- ADR 0002 pair; explicit "Not played"
  played_source    boolean,                               -- inferred by sync only (v0.2)
  acquired_on      date,
  tags             text[] not null default '{}',          -- lowercase, app-normalized
  notes            text,                                  -- always private
  visibility       visibility not null default 'private',
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  unique nulls not distinct (user_id, media_item_id, edition_id)
);
create index library_entries_user_idx on library_entries (user_id, updated_at desc);
create index library_entries_tags_idx on library_entries using gin (tags);

create table experiences (
  id               bigint generated always as identity primary key,
  public_id        uuid not null unique default uuidv7(),
  user_id          bigint not null references users on delete cascade,
  media_item_id    bigint not null references media_items,
  edition_id       bigint references editions,
  state            experience_state not null default 'active',
  started_on       date,
  finished_on      date,                                  -- finished or abandoned; null = date unknown
  progress_unit    progress_unit,
  progress_current numeric(10,2),
  progress_total   numeric(10,2),
  notes            text,
  visibility       visibility not null default 'private',
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now()
);
create index experiences_user_state_idx on experiences (user_id, state);
create index experiences_user_item_idx on experiences (user_id, media_item_id);

create table progress_events (
  id            bigint generated always as identity primary key,
  experience_id bigint not null references experiences on delete cascade,
  occurred_at   timestamptz not null default now(),
  value         numeric(10,2) not null,
  note          text
);
create index progress_events_exp_idx on progress_events (experience_id, occurred_at);

create table reviews (
  id                bigint generated always as identity primary key,
  public_id         uuid not null unique default uuidv7(),
  user_id           bigint not null references users on delete cascade,
  media_item_id     bigint not null references media_items,
  experience_id     bigint references experiences on delete set null,
  rating            smallint check (rating between 0 and 100),
  body              text,
  contains_spoilers boolean not null default false,
  visibility        visibility not null default 'private',
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  unique (user_id, media_item_id),
  check (rating is not null or body is not null)
);

create table lists (
  id          bigint generated always as identity primary key,
  public_id   uuid not null unique default uuidv7(),
  user_id     bigint not null references users on delete cascade,
  name        text not null,
  description text,
  ranked      boolean not null default false,
  visibility  visibility not null default 'private',
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now()
);
create index lists_user_idx on lists (user_id, updated_at desc);

create table list_items (
  id            bigint generated always as identity primary key,
  list_id       bigint not null references lists on delete cascade,
  media_item_id bigint not null references media_items,
  position      int not null,
  note          text,
  created_at    timestamptz not null default now(),
  unique (list_id, media_item_id)
);
create index list_items_order_idx on list_items (list_id, position);

-- Journal. References its originating record; never duplicates its data.
create table activity_events (
  id               bigint generated always as identity primary key,
  public_id        uuid not null unique default uuidv7(),
  user_id          bigint not null references users on delete cascade,
  verb             activity_verb not null,
  media_item_id    bigint not null references media_items,
  library_entry_id bigint references library_entries on delete cascade,
  experience_id    bigint references experiences on delete cascade,
  review_id        bigint references reviews on delete cascade,
  list_id          bigint references lists on delete cascade,
  visibility       visibility not null default 'private',
  occurred_at      timestamptz not null default now(),
  created_at       timestamptz not null default now()
);
create index activity_events_user_idx on activity_events (user_id, occurred_at desc);
```

## Derived values (never stored)

| Concept | Definition |
| --- | --- |
| Effective ownership | `coalesce(ownership_user, ownership_source, 'not_owned')` |
| Played | `coalesce(played_user, played_source, exists(finished or active experience))` |
| In progress | Has an experience with `state in ('active','paused')` |
| Finished | Has an experience with `state = 'finished'` |
| Backlog | `backlog = true` and not in progress and never finished |
| Display rating | `rating_display`: `stars5` = `round(rating/20.0 * 2)/2` (half stars); `ten` = `round(rating/10.0)`; `hundred` = `rating` |

## Reference data

F2 seeds `platforms` and `platform_aliases` in a migration with a starter list of about 60 entries. It covers modern platforms (PC, Mac, Linux, PS1–PS5, Xbox family, Switch/Switch 2, Steam Deck, iOS, Android) and the retro list from the RFC (Dreamcast, Saturn, Master System, Genesis/Mega Drive, Game Gear, NES, SNES, N64, GameCube, Wii, Game Boy/Color/Advance, DS, 3DS, PSP, Vita, Atari 2600/7800/Jaguar/Lynx, Neo Geo, TurboGrafx-16/PC Engine, Arcade, DOS, Amiga, Commodore 64, ZX Spectrum, MSX). The full registry work is in v0.3.

## v0.2 — Sync (F4, migration `01900_sync.sql`)

Connected accounts and everything a sync observes. ADR 0002 applies throughout:

- Sync writes `library_entries.ownership_source` and `played_source` and nothing else on that table. New entries are created with only those two columns set, so they get the defaults `backlog = false` and `visibility = 'private'`. No other user-owned table (reviews, experiences, lists, activity) is ever written by sync. Imports don't add journal events.
- `played_source` only moves to `true` (playtime > 0). Zero or missing playtime leaves it unchanged, because a `false` source would hide the experience-derived "played".
- Playtime, last played, achievement definitions, and unlocks are source facts in source-scoped tables keyed by connected account. They are never shown as personal experiences.
- Nothing is deleted when it disappears from a provider, and missing values (`null` playtime, failed requests) never clear stored ones.
- Each sync-originated change is a `sync_events` row. `respected_override = true` means the user's `*_user` value differs from what the source reported, so the effective value didn't change.

```sql
create type connection_status as enum ('active', 'error', 'disabled');
create type sync_run_status   as enum ('running', 'succeeded', 'partial', 'failed');

create table connected_accounts (
  id                  bigint generated always as identity primary key,
  public_id           uuid not null unique default uuidv7(),
  user_id             bigint not null references users on delete cascade,
  provider            text not null,
  external_account_id text not null,
  display_name        text not null default '',
  profile_url         text,
  avatar_url          text,
  credentials         bytea,
  status              connection_status not null default 'active',
  last_error          text,
  cursor              bytea,
  last_synced_at      timestamptz,
  next_sync_at        timestamptz default now(),
  created_at          timestamptz not null default now(),
  updated_at          timestamptz not null default now(),
  unique (provider, external_account_id),
  unique (user_id, provider, external_account_id)
);
create index connected_accounts_user_idx on connected_accounts (user_id);
create index connected_accounts_due_idx on connected_accounts (next_sync_at) where status <> 'disabled';

create table sync_runs (
  id          bigint generated always as identity primary key,
  public_id   uuid not null unique default uuidv7(),
  account_id  bigint not null references connected_accounts on delete cascade,
  status      sync_run_status not null default 'running',
  counts      jsonb not null default '{}',
  warnings    text[] not null default '{}',
  error       text,
  started_at  timestamptz not null default now(),
  finished_at timestamptz
);
create index sync_runs_account_idx on sync_runs (account_id, started_at desc);

create table sync_events (
  id                 bigint generated always as identity primary key,
  run_id             bigint not null references sync_runs on delete cascade,
  account_id         bigint not null references connected_accounts on delete cascade,
  user_id            bigint not null references users on delete cascade,
  media_item_id      bigint references media_items on delete cascade,
  library_entry_id   bigint references library_entries on delete set null,
  provider_item_key  text,
  kind               text not null,
  field              text,
  before             text,
  after              text,
  respected_override boolean not null default false,
  created_at         timestamptz not null default now()
);
create index sync_events_user_idx on sync_events (user_id, created_at desc);
create index sync_events_account_idx on sync_events (account_id, created_at desc);
create index sync_events_run_idx on sync_events (run_id);

create table import_exclusions (
  id                bigint generated always as identity primary key,
  account_id        bigint not null references connected_accounts on delete cascade,
  provider_item_key text not null,
  media_item_id     bigint references media_items on delete set null,
  created_at        timestamptz not null default now(),
  unique (account_id, provider_item_key)
);

create table external_playtime (
  id                bigint generated always as identity primary key,
  account_id        bigint not null references connected_accounts on delete cascade,
  media_item_id     bigint not null references media_items on delete cascade,
  provider_item_key text not null,
  minutes_total     int not null default 0,
  minutes_2weeks    int,
  last_played_at    timestamptz,
  updated_at        timestamptz not null default now(),
  unique (account_id, provider_item_key)
);
create index external_playtime_item_idx on external_playtime (media_item_id);

create table achievements (
  id                       bigint generated always as identity primary key,
  media_item_id            bigint references media_items on delete cascade,
  provider                 text not null,
  provider_game_key        text not null,
  provider_achievement_key text not null,
  name                     text not null,
  description              text,
  icon_url                 text,
  hidden                   boolean not null default false,
  display_order            int not null default 0,
  created_at               timestamptz not null default now(),
  updated_at               timestamptz not null default now(),
  unique (provider, provider_game_key, provider_achievement_key)
);
create index achievements_item_idx on achievements (media_item_id, display_order);

create table achievement_unlocks (
  id             bigint generated always as identity primary key,
  account_id     bigint not null references connected_accounts on delete cascade,
  achievement_id bigint not null references achievements on delete cascade,
  unlocked_at    timestamptz,
  mode           text not null default 'standard',
  created_at     timestamptz not null default now(),
  unique (account_id, achievement_id, mode)
);
create index achievement_unlocks_achievement_idx on achievement_unlocks (achievement_id);
```

| Column | Notes |
| --- | --- |
| `connected_accounts.provider` | Adapter name (`steam`, later `retroachievements`). `external_account_id` is the provider's account ID (SteamID64). One external account links to at most one OpenShelf user. |
| `connected_accounts.credentials` | `internal/secrets` ciphertext (AES-256-GCM, key from `OPENSHELF_SECRET_KEY`). Null for Steam, which uses the instance API key. |
| `connected_accounts.cursor` | Opaque adapter state, replaced only after a run without an adapter error. |
| `connected_accounts.next_sync_at` | Set to now + 12 h ± 10% after a run, or now + 1 h after an error. `null` or past means due. |
| `sync_runs.counts` | `runner.Counts` as JSON (`games`, `items_resolved`, `items_created`, `entries_created`, `entries_updated`, `excluded`, `skipped`, `achievement_sets`, `unlocks`). |
| `sync_runs.warnings` | Partial-result notices from the adapter (private profile, unavailable endpoint). A run with warnings is `partial`. |
| `sync_events.kind` | `entry_created`, `needs_review` (a manual item was created for an unmatched game), `ownership_source_changed`, `played_source_changed`, `playtime_changed`, `achievement_unlocked`, `override_respected`, `excluded_skipped`, and (migration 02500) `progress_changed`, `source_finished`. |
| `sync_events.field/before/after` | Text values: `ownership` (`owned`…), `played` (`true`/`false`), `playtime_minutes`. For `achievement_unlocked`, `field` is the achievement key and `after` its name. For `override_respected`, `before` is the source value and `after` the user's value that was kept. |
| `import_exclusions.provider_item_key` | Steam appid etc. Excluded keys are skipped (playtime and achievements too), with an `excluded_skipped` event each run. |
| `achievements.media_item_id` | Nullable so definitions can exist before a game is matched. Definitions are shared per instance; unlocks are per account. |
| `achievement_unlocks` | A row means unlocked. `unlocked_at` may be null when the provider doesn't report it. `mode` is `standard`, or `hardcore`/`softcore` for RetroAchievements. |

Deleting a connected account cascades its runs, events, exclusions, playtime, and unlocks. Library entries are kept, so an unlink flow that removes imported entries must find them (via `entry_created` events) first.

## External progress (migration `02500_external_progress.sql`)

```sql
create table external_progress (
  id                bigint generated always as identity primary key,
  account_id        bigint not null references connected_accounts on delete cascade,
  media_item_id     bigint not null references media_items on delete cascade,
  provider_item_key text not null,
  fraction          double precision not null default 0,
  current_seconds   int,
  duration_seconds  int,
  finished          boolean not null default false,
  started_at        timestamptz,
  finished_at       timestamptz,
  last_activity_at  timestamptz,
  updated_at        timestamptz not null default now(),
  unique (account_id, provider_item_key)
);
create index external_progress_item_idx on external_progress (media_item_id);
```

Source-owned listening progress (Audiobookshelf), written only by the sync runner, like `external_playtime`. `fraction` is 0–1 (1 when finished). `finished` follows the source; `started_at`, `finished_at`, and `last_activity_at` are never cleared by a later observation that lacks them. It's never shown as a personal experience. Progress above 0 sets `played_source = true` on the entry (ADR 0002 rules apply unchanged).

- `sync_events.kind` adds `progress_changed` (`field = 'progress'`, `before`/`after` as fractions such as `0.4300`) and `source_finished` (`field = 'finished'`, `after` is the finish date `YYYY-MM-DD` or `true`).
- `connected_accounts.provider = 'audiobookshelf'`: `external_account_id` is the Audiobookshelf user ID, `display_name` the username, and `credentials` the encrypted API key. The server URL is instance config (`OPENSHELF_AUDIOBOOKSHELF_URL`).
- `external_ids` adds `audiobookshelf_item` (item-level, the library item ID; also the provider item key). ISBNs from Audiobookshelf stay edition-level: they match existing editions, and a manual book item gets an audiobook edition carrying them along with `duration_minutes`, authors, and narrators.

## Time to beat (migration `02400_time_to_beat.sql`)

```sql
create table game_time_to_beat (
  media_item_id      bigint primary key references media_items on delete cascade,
  source             text not null default 'igdb',
  hastily_minutes    int,
  normally_minutes   int,
  completely_minutes int,
  submissions        int,
  refreshed_at       timestamptz not null default now()
);
```

Shared community estimates for game items, from IGDB `game_time_to_beats` (seconds, rounded to minutes). Written on import/refresh when the provider returns an estimate (never deleted when it doesn't) and by the `discover_time_to_beat` backfill job. A row with all three minute columns null means IGDB had no estimate when last checked; the backfill retries rows whose `refreshed_at` is older than 30 days.

## Later releases

Deferred to v0.3: `achievement_awards` (RetroAchievements mastery/completion awards, S32) and `match_candidates` (the shared match review queue, S33). Until then, unmatched Steam games become manual items with a `needs_review` sync event. `source_records` (raw provider payloads) is dropped: source tables plus `sync_events` cover what the UI needs.

Later: `follows` (v0.4) and the ActivityPub `actors` / `remote_*` tables (v0.5).
