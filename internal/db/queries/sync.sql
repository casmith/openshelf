-- Queries used by internal/sync/runner. ADR 0002: statements that write
-- library_entries touch only ownership_source and played_source (enforced by a test).

-- name: SyncGetAccount :one
select * from connected_accounts where id = $1;

-- name: SyncStartRun :one
insert into sync_runs (account_id) values ($1) returning *;

-- name: SyncFinishRun :exec
update sync_runs
set status = @status, counts = @counts, warnings = @warnings, error = @error, finished_at = now()
where id = @id;

-- name: SyncFinishAccount :exec
update connected_accounts
set status = case when status = 'disabled' then status else @status::connection_status end,
    last_error = @last_error,
    cursor = coalesce(sqlc.narg(cursor)::bytea, cursor),
    last_synced_at = coalesce(sqlc.narg(last_synced_at)::timestamptz, last_synced_at),
    next_sync_at = @next_sync_at::timestamptz, updated_at = now()
where id = @id;

-- name: SyncDueAccounts :many
select id, provider from connected_accounts
where status <> 'disabled' and next_sync_at <= @now::timestamptz
order by next_sync_at
limit @lim::int;

-- name: SyncSetNextSync :exec
update connected_accounts set next_sync_at = @next_sync_at::timestamptz where id = @id;

-- name: SyncLockKey :exec
select pg_advisory_xact_lock(hashtextextended('sync:' || @key::text, 0));

-- name: SyncIsExcluded :one
select exists(select 1 from import_exclusions where account_id = @account_id and provider_item_key = @key)::boolean;

-- name: SyncFindItemByExternalID :one
select m.id from external_ids x
join media_items m on m.id = x.media_item_id
where x.provider = @provider and x.value = @value and m.kind = @kind;

-- name: SyncFindItemByEditionExternalID :one
select m.id from external_ids x
join editions e on e.id = x.edition_id
join media_items m on m.id = e.media_item_id
where x.provider = @provider and x.value = @value and m.kind = @kind;

-- name: SyncPlaytimeItem :one
select media_item_id from external_playtime where account_id = @account_id and provider_item_key = @key;

-- name: SyncInsertManualItem :one
insert into media_items (kind, title, sort_title, release_year)
values (@kind, @title, @sort_title, sqlc.narg(release_year)::smallint)
returning id;

-- name: SyncInsertGameDetails :exec
insert into game_details (media_item_id) values ($1) on conflict do nothing;

-- name: SyncInsertBookDetails :exec
insert into book_details (media_item_id, series_name, series_position)
values (@media_item_id, @series_name, sqlc.narg(series_position)::numeric)
on conflict do nothing;

-- name: SyncFindCreatorByName :one
select id from creators where lower(name) = lower(@name::text) order by id limit 1;

-- name: SyncInsertCreator :one
insert into creators (name, sort_name) values (@name, @sort_name) returning id;

-- name: SyncInsertCredit :exec
insert into credits (media_item_id, creator_id, role, position)
values (@media_item_id, @creator_id, @role, @position)
on conflict do nothing;

-- name: SyncInsertEdition :one
insert into editions (media_item_id, format, publisher, duration_minutes)
values (@media_item_id, @format, @publisher, @duration_minutes)
returning id;

-- name: SyncAttachEditionExternalID :exec
insert into external_ids (provider, value, url, edition_id)
values (@provider, @value, @url, @edition_id)
on conflict (provider, value) do nothing;

-- name: SyncSetItemCover :exec
update media_items set cover_image_id = @image_id where id = @id and cover_image_id is null;

-- name: SyncAttachExternalID :exec
insert into external_ids (provider, value, url, media_item_id)
values (@provider, @value, @url, @media_item_id)
on conflict (provider, value) do nothing;

-- name: SyncFindEntry :one
select id, ownership_user, ownership_source, played_user, played_source
from library_entries
where user_id = @user_id and media_item_id = @media_item_id
order by edition_id nulls first, id
limit 1
for update;

-- name: SyncInsertEntry :one
insert into library_entries (user_id, media_item_id, ownership_source, played_source)
values (@user_id, @media_item_id, @ownership_source, @played_source)
returning id;

-- name: SyncSetEntrySource :exec
update library_entries
set ownership_source = @ownership_source, played_source = @played_source
where id = @id;

-- name: SyncGetPlaytime :one
select * from external_playtime where account_id = @account_id and provider_item_key = @key for update;

-- name: SyncUpsertPlaytime :exec
insert into external_playtime (account_id, media_item_id, provider_item_key, minutes_total, minutes_2weeks, last_played_at)
values (@account_id, @media_item_id, @key, @minutes_total, @minutes_2weeks, @last_played_at)
on conflict (account_id, provider_item_key) do update
set media_item_id = excluded.media_item_id,
    minutes_total = excluded.minutes_total,
    minutes_2weeks = excluded.minutes_2weeks,
    last_played_at = coalesce(excluded.last_played_at, external_playtime.last_played_at),
    updated_at = now();

-- name: SyncGetProgress :one
select * from external_progress where account_id = @account_id and provider_item_key = @key for update;

-- name: SyncUpsertProgress :exec
insert into external_progress (account_id, media_item_id, provider_item_key, fraction, current_seconds,
                               duration_seconds, finished, started_at, finished_at, last_activity_at)
values (@account_id, @media_item_id, @key, @fraction, @current_seconds, @duration_seconds, @finished,
        @started_at, @finished_at, @last_activity_at)
on conflict (account_id, provider_item_key) do update
set media_item_id = excluded.media_item_id,
    fraction = excluded.fraction,
    current_seconds = coalesce(excluded.current_seconds, external_progress.current_seconds),
    duration_seconds = coalesce(excluded.duration_seconds, external_progress.duration_seconds),
    finished = excluded.finished,
    started_at = coalesce(external_progress.started_at, excluded.started_at),
    finished_at = coalesce(excluded.finished_at, external_progress.finished_at),
    last_activity_at = coalesce(excluded.last_activity_at, external_progress.last_activity_at),
    updated_at = now();

-- name: SyncUpsertAchievement :exec
insert into achievements (media_item_id, provider, provider_game_key, provider_achievement_key, name, description,
                          icon_url, hidden, display_order)
values (@media_item_id, @provider, @game_key, @achievement_key, @name, @description, @icon_url, @hidden, @display_order)
on conflict (provider, provider_game_key, provider_achievement_key) do update
set media_item_id = coalesce(achievements.media_item_id, excluded.media_item_id),
    name = excluded.name, description = excluded.description, icon_url = excluded.icon_url,
    hidden = excluded.hidden, display_order = excluded.display_order, updated_at = now()
where (achievements.media_item_id, achievements.name, achievements.description, achievements.icon_url,
       achievements.hidden, achievements.display_order)
  is distinct from (coalesce(achievements.media_item_id, excluded.media_item_id), excluded.name, excluded.description,
                    excluded.icon_url, excluded.hidden, excluded.display_order);

-- name: SyncAchievementIDs :many
select id, provider_achievement_key from achievements
where provider = @provider and provider_game_key = @game_key;

-- name: SyncUpsertUnlock :one
insert into achievement_unlocks (account_id, achievement_id, unlocked_at, mode)
values (@account_id, @achievement_id, @unlocked_at, @mode)
on conflict (account_id, achievement_id, mode) do update
set unlocked_at = excluded.unlocked_at
where achievement_unlocks.unlocked_at is null and excluded.unlocked_at is not null
returning (xmax = 0)::boolean as inserted;

-- name: SyncInsertEvent :exec
insert into sync_events (run_id, account_id, user_id, media_item_id, library_entry_id, provider_item_key,
                         kind, field, before, after, respected_override)
values (@run_id, @account_id, @user_id, @media_item_id, @library_entry_id, @provider_item_key,
        @kind, @field, @before, @after, @respected_override);
