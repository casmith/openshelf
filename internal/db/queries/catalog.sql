-- name: CatalogCreateItem :one
insert into media_items (kind, title, sort_title, original_title, release_date, release_year, synopsis, genres, created_by_user_id)
values (@kind, @title, @sort_title, @original_title, @release_date, @release_year, @synopsis, @genres::text[], @created_by_user_id)
returning *;

-- name: CatalogUpdateItem :one
update media_items
set title = @title,
    sort_title = @sort_title,
    original_title = @original_title,
    release_date = @release_date,
    release_year = @release_year,
    synopsis = @synopsis,
    genres = @genres::text[],
    locked_fields = @locked_fields::text[],
    updated_at = now()
where id = @id
returning *;

-- name: CatalogUpsertGameDetails :exec
insert into game_details (media_item_id, franchise) values (@media_item_id, @franchise)
on conflict (media_item_id) do update set franchise = excluded.franchise;

-- name: CatalogUpsertBookDetails :exec
insert into book_details (media_item_id, series_name, series_position) values (@media_item_id, @series_name, @series_position)
on conflict (media_item_id) do update set series_name = excluded.series_name, series_position = excluded.series_position;

-- name: CatalogUpsertFilmDetails :exec
insert into film_details (media_item_id, runtime_minutes) values (@media_item_id, @runtime_minutes)
on conflict (media_item_id) do update set runtime_minutes = excluded.runtime_minutes;

-- name: CatalogUpsertShowDetails :exec
insert into show_details (media_item_id, network, status, season_count, episode_count)
values (@media_item_id, @network, @status, @season_count, @episode_count)
on conflict (media_item_id) do update
set network = excluded.network, status = excluded.status,
    season_count = excluded.season_count, episode_count = excluded.episode_count;

-- name: CatalogDeleteCredits :exec
delete from credits where media_item_id = $1;

-- name: CatalogFindCreatorByName :one
select * from creators where lower(name) = lower(@name::text) order by id limit 1;

-- name: CatalogCreateCreator :one
insert into creators (name, sort_name) values (@name, @sort_name) returning *;

-- name: CatalogInsertCredit :exec
insert into credits (media_item_id, creator_id, role, position) values (@media_item_id, @creator_id, @role, @position)
on conflict do nothing;

-- name: CatalogGetEdition :one
select * from editions where public_id = @public_id and media_item_id = @media_item_id;

-- name: CatalogCreateEdition :one
insert into editions (media_item_id, title, format, platform_id, region, publisher, release_date, page_count, duration_minutes)
values (@media_item_id, @title, @format, @platform_id, @region, @publisher, @release_date, @page_count, @duration_minutes)
returning *;

-- name: CatalogUpdateEdition :one
update editions
set title = @title, format = @format, platform_id = @platform_id, region = @region, publisher = @publisher,
    release_date = @release_date, page_count = @page_count, duration_minutes = @duration_minutes, updated_at = now()
where id = @id
returning *;

-- name: CatalogDeleteEdition :exec
delete from editions where id = $1;
