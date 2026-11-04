-- +goose Up
-- storage is always 0–100
create type media_kind       as enum ('game', 'book', 'film', 'show');
create type edition_format   as enum (
  'hardcover', 'paperback', 'ebook', 'audiobook',          -- books
  'physical', 'digital',                                   -- games
  'bluray', 'uhd_bluray', 'dvd', 'streaming',              -- film/TV
  'other');
create type platform_kind    as enum ('console', 'handheld', 'computer', 'arcade', 'mobile', 'vr', 'other');
create type credit_role      as enum ('author', 'narrator', 'translator', 'illustrator',
                                      'director', 'writer', 'creator', 'actor',
                                      'developer', 'publisher', 'composer', 'other');
create type image_status     as enum ('pending', 'ready', 'failed');

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
