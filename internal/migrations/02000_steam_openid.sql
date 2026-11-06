-- +goose Up
create table steam_openid_nonces (
  nonce      text primary key,
  created_at timestamptz not null default now()
);
create index steam_openid_nonces_created_idx on steam_openid_nonces (created_at);
