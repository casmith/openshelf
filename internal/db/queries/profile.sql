-- Every query takes the profile owner and the viewer. The owner sees everything;
-- anyone else sees only rows whose own visibility is 'instance' or 'public'.

-- name: ProfileGetUser :one
select sqlc.embed(u),
  (select count(distinct x.media_item_id) from experiences x join media_items m on m.id = x.media_item_id
    where x.user_id = u.id and x.state = 'finished'
      and (u.id = @viewer_id::bigint or x.visibility in ('instance', 'public'))
      and (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind))::bigint as completed,
  (select count(distinct x.media_item_id) from experiences x join media_items m on m.id = x.media_item_id
    where x.user_id = u.id and x.state in ('active', 'paused')
      and (u.id = @viewer_id::bigint or x.visibility in ('instance', 'public'))
      and (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind))::bigint as in_progress,
  (select count(distinct x.media_item_id) from experiences x join media_items m on m.id = x.media_item_id
    where x.user_id = u.id and x.state = 'abandoned'
      and (u.id = @viewer_id::bigint or x.visibility in ('instance', 'public'))
      and (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind))::bigint as abandoned,
  (select count(distinct le.media_item_id) from library_entries le join media_items m on m.id = le.media_item_id
    where le.user_id = u.id and le.backlog
      and (u.id = @viewer_id::bigint or le.visibility in ('instance', 'public'))
      and (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind)
      and not exists (select 1 from experiences x
        where x.user_id = le.user_id and x.media_item_id = le.media_item_id
          and x.state in ('active', 'paused', 'finished')))::bigint as backlog
from users u
where u.username = @username and u.disabled_at is null;

-- name: ProfileInProgress :many
select m.public_id, m.kind, m.title, m.cover_image_id,
       x.state, x.progress_unit, x.progress_current, x.progress_total
from (
  select distinct on (e.media_item_id) e.media_item_id, e.state, e.progress_unit,
         e.progress_current, e.progress_total, e.updated_at, e.id
  from experiences e
  where e.user_id = @owner_id and e.state in ('active', 'paused')
    and (e.user_id = @viewer_id::bigint or e.visibility in ('instance', 'public'))
  order by e.media_item_id, (e.state = 'active') desc, e.updated_at desc, e.id desc
) x
join media_items m on m.id = x.media_item_id
where sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind
order by (x.state = 'active') desc, x.updated_at desc, x.id desc
limit @lim::int;

-- Finishes grouped per item and calendar year (dates without finished_on fall
-- back to the owner's local date of the last update), newest first.
-- name: ProfileCompleted :many
with fin as (
  select x.media_item_id,
         coalesce(x.finished_on, (x.updated_at at time zone @tz::text)::date) as day
  from experiences x
  where x.user_id = @owner_id and x.state = 'finished'
    and (x.user_id = @viewer_id::bigint or x.visibility in ('instance', 'public'))
), grp as (
  select media_item_id, extract(year from day)::int as year, max(day)::date as last_day, count(*)::int as times
  from fin
  group by media_item_id, extract(year from day)
)
select g.year, g.last_day, g.times, m.public_id, m.kind, m.title, m.cover_image_id, r.rating
from grp g
join media_items m on m.id = g.media_item_id
left join reviews r on r.user_id = @owner_id and r.media_item_id = g.media_item_id
  and (r.user_id = @viewer_id::bigint or r.visibility in ('instance', 'public'))
where (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind)
  and (sqlc.narg(before_day)::date is null
       or (g.last_day, m.public_id) < (sqlc.narg(before_day)::date, sqlc.narg(before_id)::uuid))
order by g.last_day desc, m.public_id desc
limit @lim::int;

-- Backlog in the derived sense: flagged, not in progress, never finished.
-- name: ProfileBacklog :many
select m.public_id, m.kind, m.title, m.cover_image_id
from (
  select le.media_item_id, max(le.created_at) as added_at
  from library_entries le
  where le.user_id = @owner_id and le.backlog
    and (le.user_id = @viewer_id::bigint or le.visibility in ('instance', 'public'))
    and not exists (select 1 from experiences x
      where x.user_id = le.user_id and x.media_item_id = le.media_item_id
        and x.state in ('active', 'paused', 'finished'))
  group by le.media_item_id
) b
join media_items m on m.id = b.media_item_id
where sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind
order by b.added_at desc, m.id desc
limit @lim::int;

-- name: ProfileStalled :many
select m.public_id, m.kind, m.title, x.state, x.day
from (
  select distinct on (e.media_item_id) e.media_item_id, e.state, e.updated_at, e.id,
         coalesce(e.finished_on, (e.updated_at at time zone @tz::text)::date)::date as day
  from experiences e
  where e.user_id = @owner_id and e.state in ('paused', 'abandoned')
    and (e.user_id = @viewer_id::bigint or e.visibility in ('instance', 'public'))
  order by e.media_item_id, e.updated_at desc, e.id desc
) x
join media_items m on m.id = x.media_item_id
where sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind
order by x.updated_at desc, x.id desc
limit @lim::int;

-- name: ProfileReviews :many
select sqlc.embed(r), m.public_id as item_public_id, m.kind as item_kind, m.title as item_title
from reviews r
join media_items m on m.id = r.media_item_id
where r.user_id = @owner_id
  and (r.user_id = @viewer_id::bigint or r.visibility in ('instance', 'public'))
  and (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind)
order by r.updated_at desc, r.id desc
limit @lim::int;

-- name: ProfileLists :many
select l.public_id, l.name, l.ranked, l.visibility,
       (select count(*) from list_items li where li.list_id = l.id)::bigint as item_count
from lists l
where l.user_id = @owner_id
  and (l.user_id = @viewer_id::bigint or l.visibility in ('instance', 'public'))
  and (sqlc.narg(kind)::media_kind is null or exists (
    select 1 from list_items li join media_items m on m.id = li.media_item_id
    where li.list_id = l.id and m.kind = sqlc.narg(kind)::media_kind))
order by l.updated_at desc, l.id desc
limit @lim::int;
