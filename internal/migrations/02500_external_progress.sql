-- +goose Up
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
