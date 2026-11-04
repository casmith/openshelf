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
