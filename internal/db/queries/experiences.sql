-- name: ExperiencesListForItem :many
select * from experiences
where user_id = @user_id and media_item_id = @media_item_id
order by coalesce(started_on, finished_on) desc nulls last, id desc;

-- name: ExperiencesActiveForItems :many
-- Providers are empty when no linked account reports playtime or progress for the item.
select distinct on (e.media_item_id) sqlc.embed(e),
       coalesce(pt.minutes, 0)::int as playtime_minutes, coalesce(pt.provider, '')::text as playtime_provider,
       coalesce(pr.fraction, 0)::float8 as progress_fraction, coalesce(pr.finished, false)::bool as progress_finished,
       coalesce(pr.provider, '')::text as progress_provider
from experiences e
left join lateral (
  select sum(ep.minutes_total)::int as minutes, min(a.provider)::text as provider
  from external_playtime ep
  join connected_accounts a on a.id = ep.account_id
  where a.user_id = e.user_id and ep.media_item_id = e.media_item_id
) pt on true
left join lateral (
  select max(xp.fraction)::float8 as fraction, bool_or(xp.finished)::bool as finished, min(a.provider)::text as provider
  from external_progress xp
  join connected_accounts a on a.id = xp.account_id
  where a.user_id = e.user_id and xp.media_item_id = e.media_item_id
) pr on true
where e.user_id = @user_id and e.media_item_id = any(@media_item_ids::bigint[]) and e.state = 'active'
order by e.media_item_id, e.started_on desc nulls last, e.id desc;

-- name: ExperiencesGetForUpdate :one
select * from experiences
where public_id = @public_id and user_id = @user_id
for update;

-- name: ExperiencesGet :one
select * from experiences
where public_id = @public_id and user_id = @user_id;

-- name: ExperiencesInsert :one
insert into experiences (user_id, media_item_id, edition_id, state, started_on, finished_on,
                         progress_unit, progress_current, progress_total, visibility)
values (@user_id, @media_item_id, @edition_id, @state, @started_on, @finished_on,
        @progress_unit, @progress_current, @progress_total, @visibility)
returning *;

-- name: ExperiencesUpdateState :one
update experiences
set state = @state, started_on = @started_on, finished_on = @finished_on,
    progress_current = @progress_current, updated_at = @updated_at
where id = @id and user_id = @user_id
returning *;

-- name: ExperiencesUpdateProgress :one
update experiences
set progress_current = @progress_current, updated_at = @updated_at
where id = @id and user_id = @user_id
returning *;

-- name: ExperiencesUpdateDetails :one
update experiences
set started_on = @started_on, finished_on = @finished_on, progress_unit = @progress_unit,
    progress_current = @progress_current, progress_total = @progress_total,
    notes = @notes, visibility = @visibility, updated_at = @updated_at
where id = @id and user_id = @user_id
returning *;

-- name: ExperiencesInsertProgressEvent :one
insert into progress_events (experience_id, occurred_at, value)
select e.id, @occurred_at, @value
from experiences e
where e.id = @experience_id and e.user_id = @user_id
returning *;

-- name: ExperiencesDelete :one
delete from experiences
where public_id = @public_id and user_id = @user_id
returning media_item_id;

-- name: ExperiencesItemEditions :many
select id, public_id, page_count from editions
where media_item_id = @media_item_id
order by id;

-- name: ExperiencesLibraryEditionID :one
select edition_id from library_entries
where user_id = @user_id and media_item_id = @media_item_id and edition_id is not null
order by updated_at desc
limit 1;

-- name: ExperiencesShowEpisodeCount :one
select episode_count from show_details where media_item_id = @media_item_id;

-- name: ExperiencesHasEvent :one
select exists (
  select 1 from activity_events
  where user_id = @user_id and experience_id = @experience_id and verb = @verb
)::bool;

-- name: ExperiencesSynced :many
select i.id::bigint as media_item_id,
       coalesce(pt.minutes, 0)::int as playtime_minutes, coalesce(pt.provider, '')::text as playtime_provider,
       coalesce(pr.fraction, 0)::float8 as progress_fraction, coalesce(pr.finished, false)::bool as progress_finished,
       coalesce(pr.provider, '')::text as progress_provider
from unnest(@media_item_ids::bigint[]) as i(id)
left join lateral (
  select sum(ep.minutes_total)::int as minutes, min(a.provider)::text as provider
  from external_playtime ep
  join connected_accounts a on a.id = ep.account_id
  where a.user_id = @user_id and ep.media_item_id = i.id
) pt on true
left join lateral (
  select max(xp.fraction)::float8 as fraction, bool_or(xp.finished)::bool as finished, min(a.provider)::text as provider
  from external_progress xp
  join connected_accounts a on a.id = xp.account_id
  where a.user_id = @user_id and xp.media_item_id = i.id
) pr on true;
