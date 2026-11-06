-- name: DiscoverLockExternalID :exec
select pg_advisory_xact_lock(hashtextextended(@provider::text || ':' || @value::text, 0));

-- name: DiscoverFindItemsByExternalIDs :many
select x.provider, x.value, m.public_id
from external_ids x
join media_items m on m.id = x.media_item_id
join (select unnest(@providers::text[]) as provider, unnest(@vals::text[]) as value) k
  on k.provider = x.provider and k.value = x.value;

-- name: DiscoverInsertItem :one
insert into media_items (kind, title, sort_title, original_title, release_date, release_year, synopsis, genres,
                         cover_image_id, metadata_provider, metadata_refreshed_at, created_by_user_id)
values (@kind, @title, @sort_title, @original_title, @release_date, @release_year, @synopsis, @genres,
        @cover_image_id, @metadata_provider, @metadata_refreshed_at, @created_by_user_id)
returning *;

-- name: DiscoverGetItemForUpdate :one
select * from media_items where id = $1 for update;

-- name: DiscoverUpdateItem :one
update media_items
set title = @title, sort_title = @sort_title, original_title = @original_title,
    release_date = @release_date, release_year = @release_year, synopsis = @synopsis, genres = @genres,
    cover_image_id = @cover_image_id, metadata_provider = @metadata_provider,
    metadata_refreshed_at = @metadata_refreshed_at, updated_at = @metadata_refreshed_at
where id = @id
returning *;

-- name: DiscoverUpsertGameDetails :exec
insert into game_details (media_item_id, franchise) values (@media_item_id, @franchise)
on conflict (media_item_id) do update set franchise = excluded.franchise;

-- name: DiscoverUpsertBookDetails :exec
insert into book_details (media_item_id, series_name, series_position)
values (@media_item_id, @series_name, @series_position)
on conflict (media_item_id) do update
set series_name = excluded.series_name, series_position = excluded.series_position;

-- name: DiscoverUpsertFilmDetails :exec
insert into film_details (media_item_id, runtime_minutes) values (@media_item_id, @runtime_minutes)
on conflict (media_item_id) do update set runtime_minutes = excluded.runtime_minutes;

-- name: DiscoverUpsertShowDetails :exec
insert into show_details (media_item_id, network, status, season_count, episode_count)
values (@media_item_id, @network, @status, @season_count, @episode_count)
on conflict (media_item_id) do update
set network = excluded.network, status = excluded.status,
    season_count = excluded.season_count, episode_count = excluded.episode_count;

-- name: DiscoverFindCreatorByName :one
select id from creators where lower(name) = lower(@name::text) order by id limit 1;

-- name: DiscoverInsertCreator :one
insert into creators (name, sort_name) values (@name, @sort_name) returning id;

-- name: DiscoverDeleteCredits :exec
delete from credits where media_item_id = $1;

-- name: DiscoverInsertCredit :exec
insert into credits (media_item_id, creator_id, role, position)
values (@media_item_id, @creator_id, @role, @position)
on conflict do nothing;

-- name: DiscoverFindEditionByExternalID :one
select e.id from editions e
join external_ids x on x.edition_id = e.id
where e.media_item_id = @media_item_id and x.provider = @provider and x.value = @value
limit 1;

-- name: DiscoverInsertEdition :one
insert into editions (media_item_id, title, format, platform_id, region, publisher, release_date,
                      page_count, duration_minutes, cover_image_id)
values (@media_item_id, @title, @format, @platform_id, @region, @publisher, @release_date,
        @page_count, @duration_minutes, @cover_image_id)
returning id;

-- name: DiscoverUpdateEdition :exec
update editions
set title = @title, format = @format, platform_id = coalesce(@platform_id, platform_id), region = @region,
    publisher = @publisher, release_date = @release_date, page_count = @page_count,
    duration_minutes = @duration_minutes, cover_image_id = coalesce(@cover_image_id, cover_image_id),
    updated_at = now()
where id = @id;

-- name: DiscoverUpsertItemExternalID :exec
insert into external_ids (provider, value, url, media_item_id)
values (@provider, @value, @url, @media_item_id)
on conflict (provider, value) do update
set last_seen_at = now(), url = coalesce(excluded.url, external_ids.url);

-- name: DiscoverUpsertEditionExternalID :exec
insert into external_ids (provider, value, url, edition_id)
values (@provider, @value, @url, @edition_id)
on conflict (provider, value) do update
set last_seen_at = now(), url = coalesce(excluded.url, external_ids.url);

-- name: DiscoverFindPlatformByExternalID :one
select platform_id::bigint from external_ids
where provider = @provider and value = @value and platform_id is not null;

-- name: DiscoverInsertPlatformExternalID :exec
insert into external_ids (provider, value, url, platform_id)
values (@provider, @value, @url, @platform_id)
on conflict (provider, value) do nothing;

-- name: DiscoverUpsertSource :exec
insert into discover_sources (media_item_id, provider, external_id)
values (@media_item_id, @provider, @external_id)
on conflict (media_item_id) do update set provider = excluded.provider, external_id = excluded.external_id;

-- name: DiscoverGetSource :one
select * from discover_sources where media_item_id = $1;

-- name: DiscoverListStale :many
select s.media_item_id from discover_sources s
join media_items m on m.id = s.media_item_id
where m.metadata_refreshed_at is null or m.metadata_refreshed_at < @cutoff::timestamptz
order by m.metadata_refreshed_at nulls first, m.id
limit @lim::int;

-- name: DiscoverItemsMissingCovers :many
select id, kind, title, release_year from media_items
where cover_image_id is null and not ('cover_image_id' = any(locked_fields))
order by id
limit @lim::int;

-- name: DiscoverSetCoverIfMissing :exec
update media_items set cover_image_id = @image_id, updated_at = now()
where id = @id and cover_image_id is null;

-- name: DiscoverUpsertTimeToBeat :exec
insert into game_time_to_beat (media_item_id, source, hastily_minutes, normally_minutes, completely_minutes, submissions, refreshed_at)
values (@media_item_id, @source, @hastily_minutes, @normally_minutes, @completely_minutes, @submissions, now())
on conflict (media_item_id) do update
set source = excluded.source, hastily_minutes = excluded.hastily_minutes, normally_minutes = excluded.normally_minutes,
    completely_minutes = excluded.completely_minutes, submissions = excluded.submissions, refreshed_at = now();

-- name: DiscoverMarkTimeToBeatChecked :exec
insert into game_time_to_beat (media_item_id)
select unnest(@ids::bigint[])
on conflict (media_item_id) do update set refreshed_at = now();

-- name: DiscoverItemsNeedingTimeToBeat :many
select distinct on (m.id) m.id, x.value as igdb_id from media_items m
join external_ids x on x.media_item_id = m.id and x.provider = 'igdb_game'
left join game_time_to_beat t on t.media_item_id = m.id
where m.kind = 'game' and (t.media_item_id is null or t.refreshed_at < @cutoff::timestamptz)
order by m.id, x.id
limit @lim::int;
