-- +goose Up
-- The provider and ID an item was imported from, so refresh knows what to fetch.
create table discover_sources (
  media_item_id bigint primary key references media_items on delete cascade,
  provider      text not null,
  external_id   text not null
);
