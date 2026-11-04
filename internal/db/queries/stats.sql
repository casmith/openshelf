-- Period bounds are half-open; null means unbounded. Dates bound finished_on,
-- timestamps (the viewer's local midnight) bound created_at and occurred_at.

-- name: StatsCompletedByKind :many
select m.kind, count(distinct e.media_item_id)::int as items
from experiences e
join media_items m on m.id = e.media_item_id
where e.user_id = @user_id
  and e.state = 'finished'
  and e.finished_on is not null
  and (sqlc.narg(from_date)::date is null or e.finished_on >= sqlc.narg(from_date)::date)
  and (sqlc.narg(to_date)::date is null or e.finished_on < sqlc.narg(to_date)::date)
group by m.kind
order by m.kind;

-- name: StatsCompletionsByMonth :many
select date_trunc('month', e.finished_on)::date as month, m.kind, count(distinct e.media_item_id)::int as items
from experiences e
join media_items m on m.id = e.media_item_id
where e.user_id = @user_id
  and e.state = 'finished'
  and e.finished_on is not null
  and (sqlc.narg(from_date)::date is null or e.finished_on >= sqlc.narg(from_date)::date)
  and (sqlc.narg(to_date)::date is null or e.finished_on < sqlc.narg(to_date)::date)
group by 1, 2
order by 1, 2;

-- name: StatsRatingCounts :many
select r.rating::smallint as rating, count(*)::int as n
from reviews r
where r.user_id = @user_id
  and r.rating is not null
  and (sqlc.narg(from_ts)::timestamptz is null or r.created_at >= sqlc.narg(from_ts)::timestamptz)
  and (sqlc.narg(to_ts)::timestamptz is null or r.created_at < sqlc.narg(to_ts)::timestamptz)
group by r.rating
order by r.rating;

-- Progress values are positions, so an experience contributes how far it moved
-- during the period. Finished experiences with no logged events count their
-- recorded progress when they finished inside the period.
-- name: StatsProgressTotals :one
with x as (
  select e.progress_unit,
         exists (select 1 from progress_events pe where pe.experience_id = e.id) as has_events,
         coalesce((select pe.value from progress_events pe
                   where pe.experience_id = e.id
                     and (sqlc.narg(to_ts)::timestamptz is null or pe.occurred_at < sqlc.narg(to_ts)::timestamptz)
                   order by pe.occurred_at desc, pe.id desc limit 1), 0) as end_value,
         coalesce((select pe.value from progress_events pe
                   where pe.experience_id = e.id
                     and pe.occurred_at < sqlc.narg(from_ts)::timestamptz
                   order by pe.occurred_at desc, pe.id desc limit 1), 0) as start_value,
         (e.state = 'finished' and e.finished_on is not null
           and (sqlc.narg(from_date)::date is null or e.finished_on >= sqlc.narg(from_date)::date)
           and (sqlc.narg(to_date)::date is null or e.finished_on < sqlc.narg(to_date)::date)) as finished_in_period,
         coalesce(e.progress_current, e.progress_total, 0) as fallback
  from experiences e
  where e.user_id = @user_id
    and e.progress_unit in ('pages', 'minutes', 'hours')
), amounts as (
  select progress_unit,
         case when has_events then greatest(end_value - start_value, 0)
              when finished_in_period then fallback
              else 0 end as amount
  from x
)
select coalesce(sum(amount) filter (where progress_unit = 'pages'), 0)::float8 as pages,
       coalesce(sum(case progress_unit when 'minutes' then amount when 'hours' then amount * 60 end), 0)::float8 as minutes
from amounts;

-- name: StatsTopGenres :many
select g.genre::text as genre, count(distinct m.id)::int as items
from experiences e
join media_items m on m.id = e.media_item_id
cross join lateral unnest(m.genres) as g(genre)
where e.user_id = @user_id
  and e.state = 'finished'
  and e.finished_on is not null
  and (sqlc.narg(from_date)::date is null or e.finished_on >= sqlc.narg(from_date)::date)
  and (sqlc.narg(to_date)::date is null or e.finished_on < sqlc.narg(to_date)::date)
group by g.genre
order by items desc, genre
limit @lim::int;
