-- Queries used by internal/synchistory. Every read is scoped to the viewer's
-- connected accounts. This file never writes library_entries (the sync guard
-- test reads sync*.sql); unlinking deletes entries through library queries.

-- name: SynchistoryGetAccount :one
select * from connected_accounts where public_id = @public_id and user_id = @user_id;

-- name: SynchistoryListRuns :many
select id, public_id, status, counts, warnings, error, started_at, finished_at,
       (select count(*) from sync_events ev where ev.run_id = r.id)::int as event_count
from sync_runs r
where r.account_id = @account_id
  and (not @has_cursor::boolean or (r.started_at, r.id) < (@cursor_started_at::timestamptz, @cursor_id::bigint))
order by r.started_at desc, r.id desc
limit @lim::int;

-- name: SynchistoryGetRun :one
select id, public_id, started_at from sync_runs where public_id = @public_id and account_id = @account_id;

-- name: SynchistoryRunEvents :many
select ev.id, ev.kind, ev.field, ev.before, ev.after, ev.respected_override, ev.provider_item_key, ev.created_at,
       m.public_id as item_public_id, m.title as item_title
from sync_events ev
left join media_items m on m.id = ev.media_item_id
where ev.run_id = @run_id and ev.account_id = @account_id and ev.user_id = @user_id
order by ev.id
limit @lim::int;

-- name: SynchistoryEventsOfKind :many
select ev.id, ev.kind, ev.field, ev.before, ev.after, ev.respected_override, ev.provider_item_key, ev.created_at,
       m.public_id as item_public_id, m.title as item_title
from sync_events ev
left join media_items m on m.id = ev.media_item_id
where ev.account_id = @account_id and ev.user_id = @user_id and ev.kind = @kind
order by ev.created_at desc, ev.id desc
limit @lim::int;

-- name: SynchistoryListExclusions :many
select x.provider_item_key, x.created_at, m.public_id as item_public_id, m.title as item_title
from import_exclusions x
left join media_items m on m.id = x.media_item_id
where x.account_id = @account_id
order by x.created_at desc, x.id desc;

-- name: SynchistoryExclude :exec
insert into import_exclusions (account_id, provider_item_key, media_item_id)
values (@account_id, @key,
        coalesce(sqlc.narg(media_item_id)::bigint,
                 (select media_item_id from external_playtime where account_id = @account_id and provider_item_key = @key),
                 (select media_item_id from external_progress where account_id = @account_id and provider_item_key = @key)))
on conflict (account_id, provider_item_key) do nothing;

-- name: SynchistoryInclude :execrows
delete from import_exclusions where account_id = @account_id and provider_item_key = @key;

-- name: SynchistoryExcludeRemovedItem :exec
-- After the viewer deletes their last entry for an item, keep each linked account's
-- import of it deleted.
insert into import_exclusions (account_id, provider_item_key, media_item_id)
select distinct k.account_id, k.provider_item_key, m.id
from media_items m
cross join lateral (
  select ep.account_id, ep.provider_item_key from external_playtime ep where ep.media_item_id = m.id
  union
  select ep.account_id, ep.provider_item_key from external_progress ep where ep.media_item_id = m.id
  union
  select ev.account_id, ev.provider_item_key from sync_events ev
  where ev.media_item_id = m.id and ev.user_id = @user_id and ev.kind = 'entry_created' and ev.provider_item_key is not null
) k
join connected_accounts a on a.id = k.account_id and a.user_id = @user_id
where m.public_id = @item_public_id
  and not exists (select 1 from library_entries le where le.user_id = @user_id and le.media_item_id = m.id)
on conflict (account_id, provider_item_key) do nothing;

-- name: SynchistoryItemSources :many
select a.public_id as account_public_id, a.provider, a.display_name, a.external_account_id, a.last_synced_at,
       ep.provider_item_key, ep.minutes_total, ep.minutes_2weeks, ep.last_played_at,
       exists(select 1 from import_exclusions x where x.account_id = a.id and x.provider_item_key = ep.provider_item_key)::boolean as excluded,
       c.total, c.unlocked
from external_playtime ep
join connected_accounts a on a.id = ep.account_id
cross join lateral (
  select count(*)::int as total, count(u.achievement_id)::int as unlocked
  from achievements ach
  left join lateral (
    select au.achievement_id from achievement_unlocks au
    where au.achievement_id = ach.id and au.account_id = a.id limit 1
  ) u on true
  where ach.provider = a.provider and ach.provider_game_key = ep.provider_item_key
) c
where a.user_id = @user_id and ep.media_item_id = @media_item_id
order by a.provider, a.created_at, ep.provider_item_key;

-- name: SynchistoryItemProgress :many
select a.public_id as account_public_id, a.provider, a.display_name, a.external_account_id, a.last_synced_at,
       ep.provider_item_key, ep.fraction, ep.current_seconds, ep.duration_seconds, ep.finished, ep.finished_at,
       ep.last_activity_at,
       exists(select 1 from import_exclusions x where x.account_id = a.id and x.provider_item_key = ep.provider_item_key)::boolean as excluded
from external_progress ep
join connected_accounts a on a.id = ep.account_id
where a.user_id = @user_id and ep.media_item_id = @media_item_id
order by a.provider, a.created_at, ep.provider_item_key;

-- name: SynchistoryUnlinkEntries :many
-- Entries this account created, and whether they carry user data that unlinking must keep.
select le.public_id, m.title, m.public_id as item_public_id,
       (le.ownership_user is not null or le.played_user is not null or le.backlog or le.notes is not null
        or cardinality(le.tags) > 0
        or exists(select 1 from experiences ex where ex.user_id = le.user_id and ex.media_item_id = le.media_item_id)
        or exists(select 1 from reviews r where r.user_id = le.user_id and r.media_item_id = le.media_item_id))::boolean as has_user_data,
       (exists(select 1 from external_playtime ep join connected_accounts o on o.id = ep.account_id
               where ep.media_item_id = le.media_item_id and o.user_id = le.user_id and o.id <> @account_id)
        or exists(select 1 from external_progress ep join connected_accounts o on o.id = ep.account_id
                  where ep.media_item_id = le.media_item_id and o.user_id = le.user_id and o.id <> @account_id))::boolean as other_account
from library_entries le
join media_items m on m.id = le.media_item_id
where le.user_id = @user_id
  and le.id in (select ev.library_entry_id from sync_events ev
                where ev.account_id = @account_id and ev.user_id = @user_id and ev.kind = 'entry_created')
order by m.sort_title, le.id;

-- name: SynchistoryDeleteAccount :execrows
delete from connected_accounts where id = @id and user_id = @user_id;
