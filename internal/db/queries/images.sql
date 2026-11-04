-- name: ImagesUpsertByURL :one
insert into images (source_url) values (@source_url::text)
on conflict (source_url) do update set source_url = excluded.source_url
returning id, status;

-- name: ImagesMarkReady :exec
update images
set sha256 = @sha256, content_type = @content_type, width = @width, height = @height,
    status = 'ready', error = null, fetched_at = @fetched_at
where id = @id;

-- name: ImagesMarkFailed :exec
update images set status = 'failed', error = @error, fetched_at = @fetched_at where id = @id;

-- name: ImagesInsertUpload :one
insert into images (sha256, content_type, width, height, status, fetched_at)
values (@sha256, @content_type, @width, @height, 'ready', @fetched_at)
returning id;

-- name: ImagesSetItemCover :exec
update media_items set cover_image_id = @image_id, updated_at = now() where id = @id;
