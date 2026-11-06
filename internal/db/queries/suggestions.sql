-- Queries used by internal/suggestions. Everything is scoped to the viewer's own
-- connected accounts; this package never writes sync tables.

-- name: SuggestionsCandidates :many
-- One row per unfinished, undismissed game with unlocks or with playtime past the
-- main-story estimate. Only achievements whose text could signal an ending are
-- returned (as JSON); the Go classifier decides. maybe marks the playtime signal:
-- at least normally_minutes and under 3x completely (or normally) to skip endless games.
with accts as (
  select ca.id from connected_accounts ca where ca.user_id = @user_id
),
unlocked as (
  select a.id, a.media_item_id, a.provider, a.provider_game_key, a.name, a.description,
         min(u.unlocked_at) as unlocked_at
  from achievement_unlocks u
  join achievements a on a.id = u.achievement_id
  where u.account_id in (select id from accts)
    and a.media_item_id is not null
    and (sqlc.narg(item_id)::bigint is null or a.media_item_id = sqlc.narg(item_id)::bigint)
  group by a.id
),
played as (
  select p.media_item_id, sum(p.minutes_total)::int as minutes, max(p.last_played_at)::timestamptz as last_played_at
  from external_playtime p
  where p.account_id in (select id from accts)
    and (sqlc.narg(item_id)::bigint is null or p.media_item_id = sqlc.narg(item_id)::bigint)
  group by p.media_item_id
),
maybe as (
  select pl.media_item_id, t.normally_minutes
  from played pl
  join game_time_to_beat t on t.media_item_id = pl.media_item_id
  where t.normally_minutes >= 30
    and pl.minutes >= t.normally_minutes
    and pl.minutes < 3 * coalesce(t.completely_minutes, t.normally_minutes)
),
games as (
  select c.media_item_id
  from (select media_item_id from unlocked union select media_item_id from maybe) c
  where not exists (select 1 from experiences e
                    where e.user_id = @user_id and e.media_item_id = c.media_item_id and e.state = 'finished')
    and not exists (select 1 from finish_suggestion_dismissals x
                    where x.user_id = @user_id and x.media_item_id = c.media_item_id)
)
select m.id, m.public_id, m.title, m.kind, m.cover_image_id,
       (select count(*) from unlocked un where un.media_item_id = m.id)::int as unlocked,
       (select count(*) from achievements a
        where exists (select 1 from unlocked un
                      where un.media_item_id = m.id and un.provider = a.provider
                        and un.provider_game_key = a.provider_game_key))::int as total,
       coalesce(pl.minutes, 0)::int as minutes,
       pl.last_played_at,
       mb.normally_minutes as maybe_normally_minutes,
       jsonb_build_object('name', coalesce(last.name, ''), 'description', coalesce(last.description, ''), 'at', last.unlocked_at) as latest,
       coalesce((select jsonb_agg(jsonb_build_object('name', un.name, 'description', coalesce(un.description, ''), 'at', un.unlocked_at)
                                  order by un.unlocked_at nulls last, un.id)
                 from unlocked un
                 where un.media_item_id = m.id
                   and (un.name || ' ' || coalesce(un.description, '')) ~* '(end|epilogue|credit|final|beat|finish|complet)'),
                '[]')::jsonb as candidates
from games g
join media_items m on m.id = g.media_item_id
left join played pl on pl.media_item_id = m.id
left join maybe mb on mb.media_item_id = m.id
left join lateral (
  select un.name, un.description, un.unlocked_at from unlocked un
  where un.media_item_id = m.id
  order by un.unlocked_at desc nulls last, un.id desc
  limit 1
) last on true
order by m.sort_title, m.id;

-- name: SuggestionsHasAccounts :one
select exists (select 1 from connected_accounts where user_id = @user_id)::bool;

-- name: SuggestionsDismiss :exec
insert into finish_suggestion_dismissals (user_id, media_item_id)
values (@user_id, @media_item_id)
on conflict do nothing;
