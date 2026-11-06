-- +goose Up
create table finish_suggestion_dismissals (
  user_id       bigint not null references users on delete cascade,
  media_item_id bigint not null references media_items on delete cascade,
  created_at    timestamptz not null default now(),
  primary key (user_id, media_item_id)
);
