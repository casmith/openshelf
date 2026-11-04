-- +goose Up
create table import_jobs (
  id             bigint generated always as identity primary key,
  public_id      uuid not null unique default uuidv7(),
  user_id        bigint not null references users on delete cascade,
  format         text not null check (format in ('csv', 'json')),
  filename       text not null default '',
  content        bytea not null check (octet_length(content) <= 10485760),
  status         text not null default 'previewing'
                 check (status in ('previewing', 'previewed', 'queued', 'done', 'failed')),
  created_count  int not null default 0,
  matched_count  int not null default 0,
  skipped_count  int not null default 0,
  row_errors     jsonb not null default '[]',
  error          text,
  finished_at    timestamptz,
  created_at     timestamptz not null default now(),
  updated_at     timestamptz not null default now()
);
create index import_jobs_user_idx on import_jobs (user_id, created_at desc);
