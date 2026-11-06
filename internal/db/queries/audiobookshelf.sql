-- Links the Audiobookshelf user to the viewer, or refreshes the stored key when the
-- viewer already has it. Returns no row when another user has linked it.
-- name: AudiobookshelfLinkAccount :one
insert into connected_accounts (user_id, provider, external_account_id, display_name, credentials)
values (@user_id, 'audiobookshelf', @abs_user_id, @display_name, @credentials)
on conflict (provider, external_account_id) do update
set display_name = coalesce(nullif(excluded.display_name, ''), connected_accounts.display_name),
    credentials = excluded.credentials,
    status = case when connected_accounts.status = 'disabled' then connected_accounts.status else 'active' end,
    last_error = null,
    updated_at = now()
where connected_accounts.user_id = excluded.user_id
returning id, (xmax = 0)::boolean as inserted;
