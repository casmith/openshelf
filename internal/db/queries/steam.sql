-- name: SteamPruneNonces :exec
delete from steam_openid_nonces where created_at < @before::timestamptz;

-- name: SteamUseNonce :execrows
insert into steam_openid_nonces (nonce) values (@nonce) on conflict do nothing;

-- Links the SteamID to the viewer, or refreshes the profile when the viewer already has it.
-- Returns no row when another user has linked it.
-- name: SteamLinkAccount :one
insert into connected_accounts (user_id, provider, external_account_id, display_name, profile_url, avatar_url)
values (@user_id, 'steam', @steam_id, @display_name, @profile_url, @avatar_url)
on conflict (provider, external_account_id) do update
set display_name = coalesce(nullif(excluded.display_name, ''), connected_accounts.display_name),
    profile_url = coalesce(excluded.profile_url, connected_accounts.profile_url),
    avatar_url = coalesce(excluded.avatar_url, connected_accounts.avatar_url),
    updated_at = now()
where connected_accounts.user_id = excluded.user_id
returning id, (xmax = 0)::boolean as inserted;
