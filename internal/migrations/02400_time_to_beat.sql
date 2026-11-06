-- +goose Up
create table game_time_to_beat (
  media_item_id      bigint primary key references media_items on delete cascade,
  source             text not null default 'igdb',
  hastily_minutes    int,
  normally_minutes   int,
  completely_minutes int,
  submissions        int,
  refreshed_at       timestamptz not null default now()
);
