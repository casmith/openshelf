-- +goose Up
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
