# Data model (v0.1 contract)

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
  search                tsvector generated always as
                          (to_tsvector('simple', title || ' ' || coalesce(original_title, ''))) stored,
  created_at            timestamptz not null default now(),
  updated_at            timestamptz not null default now()
);
create index media_items_search_idx on media_items using gin (search);
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
--   imdb, igdb_game, igdb_platform, steam_app, retroachievements_game, retroachievements_system
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
  finished_on      date,                                  -- set for finished or abandoned
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

## Later releases (not in v0.1 migrations)

Defined by the v0.2+ foundation tasks: `connected_accounts`, `sync_runs`, `sync_events`, `source_records`, `import_exclusions`, `external_playtime`, `achievements`, `achievement_unlocks`, `achievement_awards`, `match_candidates`, `follows`, and the ActivityPub `actors` / `remote_*` tables.
