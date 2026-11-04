-- name: ReviewsGetOwnForItem :one
select * from reviews where user_id = @user_id and media_item_id = @media_item_id;

-- name: ReviewsGetOwnExperienceID :one
select id from experiences
where public_id = @public_id and user_id = @user_id and media_item_id = @media_item_id;

-- name: ReviewsListOwnExperiencesForItem :many
select public_id, id, state, started_on, finished_on from experiences
where user_id = @user_id and media_item_id = @media_item_id
order by coalesce(finished_on, started_on) desc nulls last, id desc;

-- name: ReviewsUpsert :one
insert into reviews (user_id, media_item_id, experience_id, rating, body, contains_spoilers, visibility)
values (@user_id, @media_item_id, @experience_id, @rating, @body, @contains_spoilers, @visibility)
on conflict (user_id, media_item_id) do update
set experience_id     = excluded.experience_id,
    rating            = excluded.rating,
    body              = excluded.body,
    contains_spoilers = excluded.contains_spoilers,
    visibility        = excluded.visibility,
    updated_at        = now()
returning *;

-- name: ReviewsSyncActivityVisibility :exec
update activity_events set visibility = @visibility
where review_id = @review_id and user_id = @user_id;

-- name: ReviewsGetVisible :one
select sqlc.embed(r), u.username, u.display_name,
       m.public_id as item_public_id, m.title as item_title, m.kind as item_kind,
       e.public_id as experience_public_id, e.state as experience_state,
       e.started_on as experience_started_on, e.finished_on as experience_finished_on
from reviews r
join users u on u.id = r.user_id
join media_items m on m.id = r.media_item_id
left join experiences e on e.id = r.experience_id
  and (e.user_id = sqlc.narg(viewer_id)::bigint or e.visibility = 'public'
       or (e.visibility = 'instance' and sqlc.narg(viewer_id)::bigint is not null))
where r.public_id = @public_id
  and (r.user_id = sqlc.narg(viewer_id)::bigint or r.visibility = 'public'
       or (r.visibility = 'instance' and sqlc.narg(viewer_id)::bigint is not null));

-- name: ReviewsListOthersForItem :many
select sqlc.embed(r), u.username, u.display_name
from reviews r
join users u on u.id = r.user_id
where r.media_item_id = @media_item_id
  and r.user_id <> @viewer_id
  and r.visibility in ('instance', 'public')
order by r.updated_at desc
limit 20;

-- name: ReviewsDeleteOwn :one
with d as (
  delete from reviews r where r.public_id = @public_id and r.user_id = @user_id
  returning r.media_item_id
)
select m.public_id from media_items m join d on d.media_item_id = m.id;
