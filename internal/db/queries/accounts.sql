-- name: AccountsLockSetup :exec
select pg_advisory_xact_lock(80800001);

-- name: AccountsUpdateProfile :one
update users set display_name = @display_name, email = sqlc.narg(email), updated_at = now()
where id = @id
returning *;

-- name: AccountsUpdatePreferences :one
update users set default_visibility = @default_visibility, rating_display = @rating_display,
  timezone = @timezone, updated_at = now()
where id = @id
returning *;

-- name: AccountsSetPassword :exec
update users set password_hash = @password_hash, updated_at = now() where id = @id;

-- name: AccountsListUsers :many
select * from users order by username;

-- name: AccountsLockActiveAdmins :many
select id from users where role = 'admin' and disabled_at is null order by id for update;

-- name: AccountsGetUserForUpdate :one
select * from users where username = @username for update;

-- name: AccountsSetRole :exec
update users set role = @role, updated_at = now() where id = @id;

-- name: AccountsSetDisabled :exec
update users set disabled_at = sqlc.narg(disabled_at), updated_at = now() where id = @id;
