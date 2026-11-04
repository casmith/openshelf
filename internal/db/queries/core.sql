-- name: GetUserByID :one
select * from users where id = $1;

-- name: GetUserByUsername :one
select * from users where username = $1;

-- name: CountUsers :one
select count(*) from users;

-- name: CreateUser :one
insert into users (username, display_name, email, password_hash, role)
values ($1, $2, $3, $4, $5)
returning *;

-- name: GetMediaItemByID :one
select * from media_items where id = $1;

-- name: GetMediaItemByPublicID :one
select * from media_items where public_id = $1;

-- name: GetMediaItemsByIDs :many
select * from media_items where id = any(@ids::bigint[]);

-- name: FindMediaItemByExternalID :one
select m.* from media_items m
join external_ids x on x.media_item_id = m.id
where x.provider = $1 and x.value = $2;

-- name: ListExternalIDsForItem :many
select * from external_ids where media_item_id = $1 order by provider;

-- name: ListEditionsForItem :many
select e.*, p.name as platform_name, p.short_name as platform_short_name, p.slug as platform_slug
from editions e
left join platforms p on p.id = e.platform_id
where e.media_item_id = $1
order by e.release_date nulls last, e.id;

-- name: ListCreditsForItems :many
select c.media_item_id, c.role, c.position, cr.id as creator_id, cr.public_id as creator_public_id, cr.name
from credits c
join creators cr on cr.id = c.creator_id
where c.media_item_id = any(@item_ids::bigint[])
order by c.media_item_id, c.role, c.position;

-- name: GetGameDetails :one
select * from game_details where media_item_id = $1;

-- name: GetBookDetails :one
select * from book_details where media_item_id = $1;

-- name: GetFilmDetails :one
select * from film_details where media_item_id = $1;

-- name: GetShowDetails :one
select * from show_details where media_item_id = $1;

-- name: ListPlatforms :many
select * from platforms order by family nulls last, generation nulls last, release_year nulls last, name;

-- name: GetPlatformByID :one
select * from platforms where id = $1;

-- name: GetPlatformBySlug :one
select * from platforms where slug = $1;

-- name: FindPlatformByName :one
select p.* from platforms p
where lower(p.name) = lower(@name::text)
   or lower(p.short_name) = lower(@name::text)
   or p.slug = lower(@name::text)
   or exists (select 1 from platform_aliases a where a.platform_id = p.id and lower(a.alias::text) = lower(@name::text))
order by p.id
limit 1;

-- name: SearchPlatforms :many
select distinct p.* from platforms p
left join platform_aliases a on a.platform_id = p.id
where p.name ilike '%' || @query::text || '%'
   or p.short_name ilike '%' || @query::text || '%'
   or a.alias::text ilike '%' || @query::text || '%'
order by p.name
limit 20;

-- name: CatalogSearch :many
select m.*,
       (ts_rank(to_tsvector('simple', m.title || ' ' || coalesce(m.original_title, '')), websearch_to_tsquery('simple', @query::text)) + similarity(m.title, @query::text))::real as rank
from media_items m
where (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind)
  and (to_tsvector('simple', m.title || ' ' || coalesce(m.original_title, '')) @@ websearch_to_tsquery('simple', @query::text)
       or m.title % @query::text
       or m.title ilike '%' || @query::text || '%')
order by (lower(m.title) = lower(@query::text)) desc, rank desc, m.sort_title
limit @lim::int;

-- name: ImageUpsertByURL :one
insert into images (source_url) values (@source_url::text)
on conflict (source_url) do update set source_url = excluded.source_url
returning id;

-- name: GetImage :one
select * from images where id = $1;

-- name: ActivityInsert :one
insert into activity_events (user_id, verb, media_item_id, library_entry_id, experience_id, review_id, list_id, visibility, occurred_at)
values (@user_id, @verb, @media_item_id, @library_entry_id, @experience_id, @review_id, @list_id, @visibility, @occurred_at)
returning *;
