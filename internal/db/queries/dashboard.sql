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
select sqlc.embed(m), x.state,
       x.progress_unit, x.progress_current, x.progress_total,
       (pr.fraction is not null)::bool as has_synced_progress, coalesce(pr.fraction, 0)::float8 as synced_fraction,
       (pt.minutes is not null)::bool as has_synced_playtime, coalesce(pt.minutes, 0)::int as synced_minutes
from (
  select distinct on (e.media_item_id) e.*
  from experiences e
  where e.user_id = @user_id and e.state in ('active', 'paused')
  order by e.media_item_id, (e.state = 'active') desc, e.updated_at desc, e.id desc
) x
join media_items m on m.id = x.media_item_id
-- Linked-account progress; experiences.Effective prefers it over manual entry.
left join lateral (
  select max(xp.fraction) as fraction from external_progress xp
  join connected_accounts a on a.id = xp.account_id
  where a.user_id = x.user_id and xp.media_item_id = x.media_item_id
) pr on true
left join lateral (
  select sum(ep.minutes_total) as minutes from external_playtime ep
  join connected_accounts a on a.id = ep.account_id
  where a.user_id = x.user_id and ep.media_item_id = x.media_item_id
) pt on true
order by (x.state = 'active') desc, x.updated_at desc, x.id desc
limit @lim::int;

-- name: DashboardRecentlyFinished :many
select m.public_id, m.kind, m.title, m.cover_image_id, x.finished_on, r.rating
from (
  select distinct on (e.media_item_id) e.media_item_id, e.finished_on, e.updated_at, e.id
  from experiences e
  where e.user_id = @user_id and e.state = 'finished' and e.finished_on is not null
  order by e.media_item_id, e.finished_on desc, e.updated_at desc, e.id desc
) x
join media_items m on m.id = x.media_item_id
left join reviews r on r.user_id = @user_id and r.media_item_id = x.media_item_id
order by x.finished_on desc, x.updated_at desc, x.id desc
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
