-- +goose Up
create type ownership        as enum ('owned', 'borrowed', 'not_owned');
create type experience_state as enum ('active', 'paused', 'finished', 'abandoned');
create type progress_unit    as enum ('percent', 'pages', 'chapters', 'minutes', 'hours', 'episodes');
create type activity_verb    as enum ('added', 'started', 'paused', 'resumed', 'progressed',
                                      'finished', 'abandoned', 'rated', 'reviewed', 'listed');

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
