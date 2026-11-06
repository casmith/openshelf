-- Definitions are shared per instance; a viewer sees a game's definitions only when
-- one of their accounts has it (playtime or an unlock), and only their own unlocks.

-- name: AchievementsForItem :many
with accts as (
  select ca.id, ca.provider, coalesce(nullif(ca.display_name, ''), ca.external_account_id) as name
  from connected_accounts ca where ca.user_id = @user_id
),
defs as (
  select a.id, a.provider, a.provider_game_key, a.provider_achievement_key, a.name, a.description,
         a.icon_url, a.hidden, a.display_order
  from achievements a where a.media_item_id = sqlc.arg(item_id)::bigint
),
owners as (
  select distinct ac.id, d.provider, d.provider_game_key
  from defs d join accts ac on ac.provider = d.provider
  where exists (select 1 from external_playtime p where p.account_id = ac.id and p.provider_item_key = d.provider_game_key)
     or exists (select 1 from achievement_unlocks u where u.account_id = ac.id and u.achievement_id = d.id)
)
select d.id, d.provider_achievement_key, d.name, d.description, d.icon_url, d.hidden,
       u.account_id, u.unlocked_at, coalesce(ac.name, '')::text as account_name,
       (select count(distinct o.id) from owners o)::int as account_count
from defs d
left join achievement_unlocks u on u.achievement_id = d.id and u.account_id in (select ac2.id from accts ac2)
left join accts ac on ac.id = u.account_id
where exists (select 1 from owners o where o.provider = d.provider and o.provider_game_key = d.provider_game_key)
order by d.display_order, d.id, u.unlocked_at nulls last, u.account_id;

-- name: AchievementsCounts :many
with accts as (
  select ca.id, ca.provider from connected_accounts ca where ca.user_id = @user_id
),
defs as (
  select a.id, a.media_item_id, a.provider, a.provider_game_key from achievements a
  where a.media_item_id = any(sqlc.arg(item_ids)::bigint[])
),
games as (
  select distinct d.provider, d.provider_game_key
  from defs d join accts ac on ac.provider = d.provider
  where exists (select 1 from external_playtime p where p.account_id = ac.id and p.provider_item_key = d.provider_game_key)
     or exists (select 1 from achievement_unlocks u where u.account_id = ac.id and u.achievement_id = d.id)
)
select d.media_item_id::bigint as media_item_id,
       count(distinct d.id)::int as total,
       count(distinct u.achievement_id)::int as unlocked
from defs d
join games g on g.provider = d.provider and g.provider_game_key = d.provider_game_key
left join achievement_unlocks u on u.achievement_id = d.id and u.account_id in (select ac2.id from accts ac2)
group by d.media_item_id;

-- name: AchievementsUnlocksForItem :many
-- The viewer's dated unlocks for an item, earliest first.
select a.name, coalesce(a.description, '')::text as description, min(u.unlocked_at)::timestamptz as unlocked_at
from achievement_unlocks u
join achievements a on a.id = u.achievement_id
join connected_accounts ca on ca.id = u.account_id
where ca.user_id = @user_id and a.media_item_id = sqlc.arg(item_id)::bigint and u.unlocked_at is not null
group by a.id
order by unlocked_at, a.id;

-- name: AchievementsLastPlayed :one
-- The viewer's most recent play of an item across their accounts.
select p.last_played_at::timestamptz as last_played_at
from external_playtime p
join connected_accounts ca on ca.id = p.account_id
where ca.user_id = @user_id and p.media_item_id = sqlc.arg(item_id)::bigint and p.last_played_at is not null
order by p.last_played_at desc
limit 1;
