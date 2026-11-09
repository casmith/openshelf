-- name: ExperiencesListForItem :many
select * from experiences
where user_id = @user_id and media_item_id = @media_item_id
order by coalesce(started_on, finished_on) desc nulls last, id desc;

-- name: ExperiencesActiveForItem :one
select * from experiences
where user_id = @user_id and media_item_id = @media_item_id and state = 'active'
order by started_on desc nulls last, id desc
limit 1;

-- name: ExperiencesActiveForItems :many
select distinct on (media_item_id) * from experiences
where user_id = @user_id and media_item_id = any(@media_item_ids::bigint[]) and state = 'active'
order by media_item_id, started_on desc nulls last, id desc;

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

-- name: ExperiencesSyncedPlaytime :many
select ep.media_item_id, sum(ep.minutes_total)::int as minutes, min(a.provider)::text as provider
from external_playtime ep
join connected_accounts a on a.id = ep.account_id
where a.user_id = @user_id and ep.media_item_id = any(@media_item_ids::bigint[])
group by ep.media_item_id;
