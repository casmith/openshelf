-- name: DashboardCounters :one
select
  (select count(distinct le.media_item_id) from library_entries le where le.user_id = @user_id)::bigint as cataloged,
  (select count(distinct x.media_item_id) from experiences x
    where x.user_id = @user_id and x.state in ('active', 'paused'))::bigint as in_progress,
  (select count(distinct le.media_item_id) from library_entries le
    where le.user_id = @user_id and le.backlog
      and not exists (select 1 from experiences x
        where x.user_id = le.user_id and x.media_item_id = le.media_item_id
          and x.state in ('active', 'paused', 'finished')))::bigint as backlog,
  (select count(distinct x.media_item_id) from experiences x
    where x.user_id = @user_id and x.state = 'finished')::bigint as completed;

-- name: DashboardCurrent :many
select sqlc.embed(m), x.state, x.progress_unit, x.progress_current, x.progress_total
from (
  select distinct on (e.media_item_id) e.*
  from experiences e
  where e.user_id = @user_id and e.state in ('active', 'paused')
  order by e.media_item_id, (e.state = 'active') desc, e.updated_at desc, e.id desc
) x
join media_items m on m.id = x.media_item_id
order by (x.state = 'active') desc, x.updated_at desc, x.id desc
limit @lim::int;

-- name: DashboardRecentlyFinished :many
select m.public_id, m.kind, m.title, m.cover_image_id, x.finished_on, r.rating
from (
  select distinct on (e.media_item_id) e.media_item_id, e.finished_on, e.updated_at, e.id
  from experiences e
  where e.user_id = @user_id and e.state = 'finished'
  order by e.media_item_id, e.finished_on desc nulls last, e.updated_at desc, e.id desc
) x
join media_items m on m.id = x.media_item_id
left join reviews r on r.user_id = @user_id and r.media_item_id = x.media_item_id
order by x.finished_on desc nulls last, x.updated_at desc, x.id desc
limit @lim::int;

-- Random but stable for a given seed (the viewer's local date).
-- name: DashboardBacklogPicks :many
select m.public_id, m.kind, m.title, m.cover_image_id
from media_items m
where m.id in (
  select le.media_item_id from library_entries le
  where le.user_id = @user_id and le.backlog
    and not exists (select 1 from experiences x
      where x.user_id = le.user_id and x.media_item_id = le.media_item_id
        and x.state in ('active', 'paused', 'finished'))
)
order by md5(m.public_id::text || @seed::text), m.id
limit @lim::int;
