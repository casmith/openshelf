-- name: JournalListEvents :many
select e.public_id, e.verb, e.occurred_at,
       m.public_id as item_public_id, m.kind, m.title, m.cover_image_id,
       l.public_id as list_public_id, l.name as list_name,
       r.rating,
       x.progress_unit, x.progress_current, x.progress_total
from activity_events e
join media_items m on m.id = e.media_item_id
left join lists l on l.id = e.list_id and l.user_id = e.user_id
left join reviews r on r.id = e.review_id and r.user_id = e.user_id
left join experiences x on x.id = e.experience_id and x.user_id = e.user_id
where e.user_id = @user_id
  and (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind)
  and (sqlc.narg(verb)::activity_verb is null or e.verb = sqlc.narg(verb)::activity_verb)
  and (sqlc.narg(before_at)::timestamptz is null
       or (e.occurred_at, e.public_id) < (sqlc.narg(before_at)::timestamptz, sqlc.narg(before_id)::uuid))
order by e.occurred_at desc, e.public_id desc
limit @lim::int;

-- name: JournalListEventsAfter :many
select e.public_id, e.verb, e.occurred_at,
       m.public_id as item_public_id, m.kind, m.title, m.cover_image_id,
       l.public_id as list_public_id, l.name as list_name,
       r.rating,
       x.progress_unit, x.progress_current, x.progress_total
from activity_events e
join media_items m on m.id = e.media_item_id
left join lists l on l.id = e.list_id and l.user_id = e.user_id
left join reviews r on r.id = e.review_id and r.user_id = e.user_id
left join experiences x on x.id = e.experience_id and x.user_id = e.user_id
where e.user_id = @user_id
  and (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind)
  and (sqlc.narg(verb)::activity_verb is null or e.verb = sqlc.narg(verb)::activity_verb)
  and (e.occurred_at, e.public_id) > (@after_at::timestamptz, @after_id::uuid)
order by e.occurred_at, e.public_id
limit @lim::int;
