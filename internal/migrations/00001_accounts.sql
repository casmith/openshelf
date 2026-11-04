-- +goose Up
create extension if not exists citext;
create extension if not exists pg_trgm;

create type user_role as enum ('admin', 'member');
create type visibility as enum ('private', 'instance', 'public');
create type rating_display as enum ('stars5', 'ten', 'hundred');

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

create table sessions (
  token  text primary key,
  data   bytea not null,
  expiry timestamptz not null
);
create index sessions_expiry_idx on sessions (expiry);
