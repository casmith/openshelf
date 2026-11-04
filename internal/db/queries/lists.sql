-- name: ListsCreate :one
insert into lists (user_id, name, description, ranked, visibility)
values (@user_id, @name, @description, @ranked, @visibility)
returning *;

-- name: ListsGetVisible :one
-- viewer_id 0 is an anonymous viewer, who can only read public lists.
select l.*, u.username as owner_username, u.display_name as owner_display_name
from lists l
join users u on u.id = l.user_id
where l.public_id = @public_id
  and (l.user_id = @viewer_id::bigint
       or l.visibility = 'public'
       or (l.visibility = 'instance' and @viewer_id::bigint > 0));

-- name: ListsGetOwned :one
select * from lists where public_id = @public_id and user_id = @user_id;

-- name: ListsLockOwned :one
select * from lists where public_id = @public_id and user_id = @user_id for update;

-- name: ListsUpdate :one
update lists
set name = @name, description = @description, ranked = @ranked, visibility = @visibility, updated_at = now()
where id = @id and user_id = @user_id
returning *;

-- name: ListsTouch :exec
update lists set updated_at = now() where id = @id and user_id = @user_id;

-- name: ListsDelete :execrows
delete from lists where id = @id and user_id = @user_id;

-- name: ListsForOwner :many
select l.*, (select count(*) from list_items li where li.list_id = l.id) as item_count
from lists l
where l.user_id = @user_id
order by l.updated_at desc, l.id desc;

-- name: ListsShared :many
select l.*, u.username as owner_username, u.display_name as owner_display_name,
       (select count(*) from list_items li where li.list_id = l.id) as item_count
from lists l
join users u on u.id = l.user_id
where l.user_id <> @viewer_id::bigint
  and l.visibility in ('instance', 'public')
  and u.disabled_at is null
order by l.updated_at desc, l.id desc;

-- name: ListsCollageCovers :many
select r.list_id, r.cover_image_id, r.kind
from (
  select li.list_id, m.cover_image_id, m.kind,
         row_number() over (partition by li.list_id order by li.position, li.id) as rn
  from list_items li
  join media_items m on m.id = li.media_item_id
  where li.list_id = any(@list_ids::bigint[])
) r
where r.rn <= 4
order by r.list_id, r.rn;

-- name: ListsItems :many
select li.id, li.position, li.note, m.id as media_item_id, m.public_id as media_public_id,
       m.kind, m.title, m.release_year, m.cover_image_id
from list_items li
join media_items m on m.id = li.media_item_id
where li.list_id = @list_id
order by li.position, li.id;

-- name: ListsItemIDs :many
select id, media_item_id from list_items where list_id = @list_id order by position, id;

-- name: ListsAddItem :one
-- Returns no row when the item is already on the list.
insert into list_items (list_id, media_item_id, position)
values (@list_id, @media_item_id,
        coalesce((select max(position) from list_items where list_id = @list_id), 0) + 1)
on conflict (list_id, media_item_id) do nothing
returning *;

-- name: ListsRemoveItem :execrows
delete from list_items where list_id = @list_id and media_item_id = @media_item_id;

-- name: ListsSetPositions :exec
update list_items li
set position = t.ord
from unnest(@ids::bigint[]) with ordinality as t(id, ord)
where li.id = t.id and li.list_id = @list_id and li.position <> t.ord;

-- name: ListsForOwnerWithItem :many
select l.public_id, l.name, l.visibility,
       exists (select 1 from list_items li where li.list_id = l.id and li.media_item_id = @media_item_id) as has_item
from lists l
where l.user_id = @user_id
order by l.updated_at desc, l.id desc;
