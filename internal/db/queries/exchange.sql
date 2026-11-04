-- name: ExchangeExportItems :many
with refs as (
  select media_item_id from library_entries where user_id = @user_id::bigint
  union select media_item_id from experiences where user_id = @user_id::bigint
  union select media_item_id from reviews where user_id = @user_id::bigint
  union select li.media_item_id from list_items li join lists l on l.id = li.list_id where l.user_id = @user_id::bigint
  union select media_item_id from activity_events where user_id = @user_id::bigint
)
select m.public_id, m.kind, m.title, m.original_title, m.release_year, m.release_date,
       coalesce((select jsonb_agg(jsonb_build_object('provider', x.provider, 'value', x.value) order by x.provider, x.value)
                 from external_ids x where x.media_item_id = m.id), '[]')::jsonb as external_ids
from media_items m
join refs on refs.media_item_id = m.id
order by m.kind, m.sort_title, m.id;

-- name: ExchangeExportEditions :many
with refs as (
  select edition_id from library_entries where user_id = @user_id::bigint and edition_id is not null
  union select edition_id from experiences where user_id = @user_id::bigint and edition_id is not null
)
select e.public_id, m.public_id as item_public_id, e.title, e.format, p.slug as platform_slug,
       e.region, e.publisher, e.release_date
from editions e
join refs on refs.edition_id = e.id
join media_items m on m.id = e.media_item_id
left join platforms p on p.id = e.platform_id
order by e.id;

-- name: ExchangeExportEntries :many
select le.public_id, m.public_id as item_public_id, e.public_id as edition_public_id,
       le.ownership_user, le.backlog, le.played_user, le.acquired_on, le.tags, le.notes,
       le.visibility, le.created_at, le.updated_at
from library_entries le
join media_items m on m.id = le.media_item_id
left join editions e on e.id = le.edition_id
where le.user_id = @user_id
order by le.id;

-- name: ExchangeExportExperiences :many
select x.public_id, m.public_id as item_public_id, e.public_id as edition_public_id,
       x.state, x.started_on, x.finished_on, x.progress_unit, x.progress_current, x.progress_total,
       x.notes, x.visibility, x.created_at, x.updated_at
from experiences x
join media_items m on m.id = x.media_item_id
left join editions e on e.id = x.edition_id
where x.user_id = @user_id
order by x.id;

-- name: ExchangeExportProgressEvents :many
select x.public_id as experience_public_id, pe.occurred_at, pe.value, pe.note
from progress_events pe
join experiences x on x.id = pe.experience_id
where x.user_id = @user_id
order by pe.experience_id, pe.occurred_at, pe.id;

-- name: ExchangeExportReviews :many
select r.public_id, m.public_id as item_public_id, x.public_id as experience_public_id,
       r.rating, r.body, r.contains_spoilers, r.visibility, r.created_at, r.updated_at
from reviews r
join media_items m on m.id = r.media_item_id
left join experiences x on x.id = r.experience_id and x.user_id = r.user_id
where r.user_id = @user_id
order by r.id;

-- name: ExchangeExportLists :many
select public_id, name, description, ranked, visibility, created_at, updated_at
from lists
where user_id = @user_id
order by id;

-- name: ExchangeExportListItems :many
select l.public_id as list_public_id, m.public_id as item_public_id, li.position, li.note
from list_items li
join lists l on l.id = li.list_id
join media_items m on m.id = li.media_item_id
where l.user_id = @user_id
order by li.list_id, li.position, li.id;

-- name: ExchangeExportActivity :many
select a.public_id, a.verb, m.public_id as item_public_id,
       le.public_id as entry_public_id, x.public_id as experience_public_id,
       r.public_id as review_public_id, l.public_id as list_public_id,
       a.visibility, a.occurred_at
from activity_events a
join media_items m on m.id = a.media_item_id
left join library_entries le on le.id = a.library_entry_id
left join experiences x on x.id = a.experience_id
left join reviews r on r.id = a.review_id
left join lists l on l.id = a.list_id
where a.user_id = @user_id
order by a.occurred_at, a.id;

-- name: ExchangeInsertJob :one
insert into import_jobs (user_id, format, filename, content, status)
values (@user_id, @format, @filename, @content, @status)
returning id, public_id;

-- name: ExchangeGetJob :one
select id, public_id, user_id, format, filename, status, created_count, matched_count, skipped_count,
       row_errors, error, finished_at, created_at
from import_jobs
where public_id = @public_id and user_id = @user_id;

-- name: ExchangeListJobs :many
select public_id, format, filename, status, created_count, matched_count, skipped_count,
       jsonb_array_length(row_errors)::int as error_count, created_at
from import_jobs
where user_id = @user_id
order by created_at desc, id desc
limit 20;

-- name: ExchangeLoadJobForWork :one
select id, user_id, format, content, status from import_jobs where id = @id for update;

-- name: ExchangeSetJobStatus :exec
update import_jobs set status = @status, updated_at = now() where id = @id;

-- name: ExchangeFinishJob :exec
update import_jobs
set status = @status, created_count = @created_count, matched_count = @matched_count,
    skipped_count = @skipped_count, row_errors = @row_errors, error = sqlc.narg(error),
    finished_at = case when @status::text in ('done', 'failed') then now() end,
    updated_at = now()
where id = @id;

-- name: ExchangeQueueJob :one
update import_jobs set status = 'queued', updated_at = now()
where public_id = @public_id and user_id = @user_id and status = 'previewed'
returning id;

-- name: ExchangeFindItemByExternalID :one
select m.* from external_ids x
left join editions e on e.id = x.edition_id
join media_items m on m.id = coalesce(x.media_item_id, e.media_item_id)
where x.provider = @provider and x.value = @value
limit 1;

-- name: ExchangeFindItemByTitle :one
select * from media_items
where kind = @kind
  and lower(title) = lower(@title::text)
  and (sqlc.narg(release_year)::smallint is null or release_year = sqlc.narg(release_year)::smallint)
order by (release_year is null) desc, id
limit 1;

-- name: ExchangeCreateItem :one
insert into media_items (kind, title, sort_title, original_title, release_year, release_date, created_by_user_id)
values (@kind, @title, @sort_title, @original_title, @release_year, @release_date, @created_by_user_id)
returning *;

-- name: ExchangeAddExternalID :exec
insert into external_ids (provider, value, media_item_id)
values (@provider, @value, @media_item_id)
on conflict (provider, value) do nothing;

-- name: ExchangeGetEditionByPublicID :one
select id from editions where public_id = @public_id and media_item_id = @media_item_id;

-- name: ExchangeFindEdition :one
select id from editions
where media_item_id = @media_item_id
  and platform_id is not distinct from sqlc.narg(platform_id)::bigint
  and format is not distinct from sqlc.narg(format)::edition_format
  and title is not distinct from sqlc.narg(title)::text
order by id
limit 1;

-- name: ExchangeCreateEdition :one
insert into editions (media_item_id, platform_id, format, title, region, publisher, release_date)
values (@media_item_id, sqlc.narg(platform_id)::bigint, sqlc.narg(format)::edition_format, sqlc.narg(title)::text,
        @region, @publisher, @release_date)
returning id;

-- name: ExchangeGetEntry :one
select * from library_entries
where user_id = @user_id and media_item_id = @media_item_id
  and edition_id is not distinct from sqlc.narg(edition_id)::bigint;

-- name: ExchangeInsertEntry :one
insert into library_entries (user_id, media_item_id, edition_id, ownership_user, backlog, played_user, acquired_on, tags, notes, visibility)
values (@user_id, @media_item_id, sqlc.narg(edition_id)::bigint, @ownership_user, @backlog, @played_user, @acquired_on, @tags, @notes, 'private')
returning *;

-- name: ExchangeUpdateEntry :exec
update library_entries
set ownership_user = @ownership_user, backlog = @backlog, played_user = @played_user,
    acquired_on = @acquired_on, tags = @tags, notes = @notes, updated_at = now()
where id = @id and user_id = @user_id;

-- name: ExchangeFindExperience :one
select id from experiences
where user_id = @user_id and media_item_id = @media_item_id
  and edition_id is not distinct from sqlc.narg(edition_id)::bigint
  and state = @state
  and started_on is not distinct from sqlc.narg(started_on)::date
  and finished_on is not distinct from sqlc.narg(finished_on)::date
order by id
limit 1;

-- name: ExchangeInsertExperience :one
insert into experiences (user_id, media_item_id, edition_id, state, started_on, finished_on,
                         progress_unit, progress_current, progress_total, notes, visibility)
values (@user_id, @media_item_id, sqlc.narg(edition_id)::bigint, @state, sqlc.narg(started_on)::date, sqlc.narg(finished_on)::date,
        @progress_unit, @progress_current, @progress_total, @notes, 'private')
returning id;

-- name: ExchangeAddProgressEvent :execrows
insert into progress_events (experience_id, occurred_at, value, note)
select x.id, @occurred_at, @value, @note
from experiences x
where x.id = @experience_id and x.user_id = @user_id
  and not exists (select 1 from progress_events pe
                  where pe.experience_id = x.id and pe.occurred_at = @occurred_at and pe.value = @value);

-- name: ExchangeGetReview :one
select * from reviews where user_id = @user_id and media_item_id = @media_item_id;

-- name: ExchangeInsertReview :one
insert into reviews (user_id, media_item_id, experience_id, rating, body, contains_spoilers, visibility)
values (@user_id, @media_item_id, @experience_id, @rating, @body, @contains_spoilers, 'private')
returning id;

-- name: ExchangeUpdateReview :exec
update reviews
set rating = @rating, body = @body, contains_spoilers = @contains_spoilers, updated_at = now()
where id = @id and user_id = @user_id;

-- name: ExchangeFindList :one
select id from lists where user_id = @user_id and name = @name order by id limit 1;

-- name: ExchangeInsertList :one
insert into lists (user_id, name, description, ranked, visibility)
values (@user_id, @name, @description, @ranked, 'private')
returning id;

-- name: ExchangeAddListItem :execrows
insert into list_items (list_id, media_item_id, position, note)
select l.id, @media_item_id,
       coalesce((select max(li.position) from list_items li where li.list_id = l.id), 0) + 1, @note
from lists l
where l.id = @list_id and l.user_id = @user_id
on conflict (list_id, media_item_id) do nothing;

-- name: ExchangeActivityExists :one
select exists (
  select 1 from activity_events
  where user_id = @user_id and verb = @verb and media_item_id = @media_item_id
    and occurred_at = @occurred_at
    and library_entry_id is not distinct from sqlc.narg(library_entry_id)::bigint
    and experience_id is not distinct from sqlc.narg(experience_id)::bigint
    and review_id is not distinct from sqlc.narg(review_id)::bigint
    and list_id is not distinct from sqlc.narg(list_id)::bigint
);

-- name: ExchangeFindEditionByPlatform :one
select id from editions
where media_item_id = @media_item_id and platform_id = @platform_id
order by (format is null and title is null) desc, id
limit 1;
