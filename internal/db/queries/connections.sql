-- name: ConnectionsListForUser :many
select a.id, a.public_id, a.provider, a.external_account_id, a.display_name, a.profile_url, a.avatar_url,
       a.status, a.last_error, a.last_synced_at, a.next_sync_at,
       coalesce(r.status::text, '')::text as run_status,
       coalesce(r.warnings, '{}')::text[] as run_warnings,
       coalesce((select c.counts from sync_runs c where c.account_id = a.id and c.status in ('succeeded', 'partial')
                 order by c.started_at desc, c.id desc limit 1), '{}')::jsonb as last_counts,
       r.finished_at as run_finished_at
from connected_accounts a
left join lateral (
  select status, warnings, finished_at from sync_runs
  where account_id = a.id order by started_at desc, id desc limit 1
) r on true
where a.user_id = @user_id
order by a.provider, a.created_at;

-- name: ConnectionsGetForUser :one
select * from connected_accounts where public_id = @public_id and user_id = @user_id;
