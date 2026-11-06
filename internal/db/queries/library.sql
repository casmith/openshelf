-- name: LibraryListEntries :many
-- Derived values follow docs/data-model.md. k_num/k_text are the keyset sort keys;
-- null_key places missing release dates, ratings, and lengths last in either direction.
with base as (
  select le.public_id, le.created_at, le.tags, le.backlog, le.media_item_id,
         coalesce(le.ownership_user, le.ownership_source, 'not_owned')::ownership as ownership,
         le.ownership_user, le.played_user, le.ownership_source, le.played_source, coalesce(src.provider, '')::text as source_provider,
         m.public_id as item_public_id, m.kind, m.title, m.sort_title, m.release_date, m.release_year, m.genres,
         coalesce(e.cover_image_id, m.cover_image_id) as cover_image_id,
         e.format as edition_format, p.slug as platform_slug, p.name as platform_name, p.short_name as platform_short_name,
         r.rating,
         ttb.normally_minutes as length_minutes,
         x.in_progress, x.finished,
         coalesce(le.played_user, le.played_source, x.played_by_experience)::boolean as played
  from library_entries le
  join media_items m on m.id = le.media_item_id
  left join editions e on e.id = le.edition_id
  left join platforms p on p.id = e.platform_id
  left join reviews r on r.user_id = le.user_id and r.media_item_id = le.media_item_id
  left join game_time_to_beat ttb on ttb.media_item_id = le.media_item_id and m.kind = 'game' and ttb.normally_minutes > 0
  cross join lateral (
    select coalesce(bool_or(ex.state in ('active', 'paused')), false)::boolean as in_progress,
           coalesce(bool_or(ex.state = 'finished'), false)::boolean as finished,
           coalesce(bool_or(ex.state in ('active', 'finished')), false)::boolean as played_by_experience
    from experiences ex
    where ex.user_id = le.user_id and ex.media_item_id = le.media_item_id
  ) x
  left join lateral (
    select a.provider from sync_events ev
    join connected_accounts a on a.id = ev.account_id
    where (le.ownership_source is not null or le.played_source is not null)
      and ev.library_entry_id = le.id and ev.user_id = le.user_id
    order by ev.id
    limit 1
  ) src on true
  where le.user_id = @user_id
    and (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind)
    and (sqlc.narg(platform)::text is null or p.slug = sqlc.narg(platform)::text)
    and (sqlc.narg(edition_format)::edition_format is null or e.format = sqlc.narg(edition_format)::edition_format)
    and (sqlc.narg(genre)::text is null or sqlc.narg(genre)::text = any(m.genres))
    and (sqlc.narg(tag)::text is null or sqlc.narg(tag)::text = any(le.tags))
    and (sqlc.narg(rating_min)::int is null or r.rating >= sqlc.narg(rating_min)::int)
    and (not @unrated::boolean or r.rating is null)
    and (not @length_filter::boolean or m.kind = 'game')
    and (sqlc.narg(added_from)::date is null or (le.created_at at time zone @tz::text)::date >= sqlc.narg(added_from)::date)
    and (sqlc.narg(added_to)::date is null or (le.created_at at time zone @tz::text)::date <= sqlc.narg(added_to)::date)
),
keyed as (
  select b.*,
         (b.backlog and not b.in_progress and not b.finished)::boolean as in_backlog,
         (case @sort::text
            when 'added' then extract(epoch from b.created_at)::float8
            when 'release' then coalesce(extract(epoch from coalesce(b.release_date, make_date(b.release_year::int, 1, 1)))::float8, @null_key::float8)
            when 'rating' then coalesce(b.rating::float8, @null_key::float8)
            when 'length' then coalesce(b.length_minutes::float8, @null_key::float8)
            else 0::float8
          end)::float8 as k_num,
         lower(b.sort_title)::text as k_text
  from base b
)
select k.public_id, k.created_at, k.tags, k.ownership, k.media_item_id,
       k.ownership_user, k.played_user, k.ownership_source, k.played_source, k.source_provider, k.item_public_id, k.kind, k.title, k.release_date, k.release_year,
       k.cover_image_id, k.edition_format, k.platform_name, k.platform_short_name, k.rating, k.length_minutes,
       k.in_progress, k.finished, k.played, k.in_backlog, k.k_num, k.k_text
from keyed k
where (sqlc.narg(ownership)::ownership is null or k.ownership = sqlc.narg(ownership)::ownership)
  and (sqlc.narg(status)::text is null
       or (sqlc.narg(status)::text = 'in_progress' and k.in_progress)
       or (sqlc.narg(status)::text = 'finished' and k.finished)
       or (sqlc.narg(status)::text = 'backlog' and k.in_backlog)
       or (sqlc.narg(status)::text = 'played' and k.played)
       or (sqlc.narg(status)::text = 'not_played' and not k.played))
  and (not @no_length::boolean or k.length_minutes is null)
  and (sqlc.narg(length_min)::int is null or k.length_minutes >= sqlc.narg(length_min)::int)
  and (sqlc.narg(length_max)::int is null or k.length_minutes < sqlc.narg(length_max)::int)
  and (not @has_cursor::boolean
       or (case when @num_desc::boolean then k.k_num < @cursor_num::float8 else k.k_num > @cursor_num::float8 end)
       or (k.k_num = @cursor_num::float8 and (case when @text_desc::boolean
             then (k.k_text, k.public_id) < (@cursor_text::text, @cursor_id::uuid)
             else (k.k_text, k.public_id) > (@cursor_text::text, @cursor_id::uuid) end)))
order by
  case when not @num_desc::boolean then k.k_num end asc,
  case when @num_desc::boolean then k.k_num end desc,
  case when not @text_desc::boolean then k.k_text end asc,
  case when @text_desc::boolean then k.k_text end desc,
  case when not @text_desc::boolean then k.public_id end asc,
  case when @text_desc::boolean then k.public_id end desc
limit @lim::int;

-- name: LibraryFacets :many
select 'platform'::text as facet, p.slug::text as value, coalesce(p.short_name, p.name)::text as label
from library_entries le
join media_items m on m.id = le.media_item_id
join editions e on e.id = le.edition_id
join platforms p on p.id = e.platform_id
where le.user_id = @user_id and (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind)
group by p.slug, p.short_name, p.name
union all
select 'format'::text, e.format::text, e.format::text
from library_entries le
join media_items m on m.id = le.media_item_id
join editions e on e.id = le.edition_id
where le.user_id = @user_id and e.format is not null and (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind)
group by e.format
union all
select 'genre'::text, g.g::text, g.g::text
from library_entries le
join media_items m on m.id = le.media_item_id
cross join unnest(m.genres) as g(g)
where le.user_id = @user_id and (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind)
group by g.g
union all
select 'tag'::text, t.t::text, t.t::text
from library_entries le
join media_items m on m.id = le.media_item_id
cross join unnest(le.tags) as t(t)
where le.user_id = @user_id and (sqlc.narg(kind)::media_kind is null or m.kind = sqlc.narg(kind)::media_kind)
group by t.t
order by 1, 3;

-- name: LibraryItemEntries :many
select le.*,
       e.public_id as edition_public_id, e.title as edition_title, e.format as edition_format, e.region as edition_region,
       p.name as platform_name, p.short_name as platform_short_name,
       coalesce(le.ownership_user, le.ownership_source, 'not_owned')::ownership as effective_ownership,
       coalesce(le.played_user, le.played_source, x.played_by_experience)::boolean as played,
       coalesce(src.provider, '')::text as source_provider,
       (exists(select 1 from external_playtime ep join connected_accounts a on a.id = ep.account_id
               where ep.media_item_id = le.media_item_id and a.user_id = le.user_id)
        or exists(select 1 from external_progress xp join connected_accounts a on a.id = xp.account_id
                  where xp.media_item_id = le.media_item_id and a.user_id = le.user_id))::boolean as synced
from library_entries le
left join editions e on e.id = le.edition_id
left join platforms p on p.id = e.platform_id
cross join lateral (
  select coalesce(bool_or(ex.state in ('active', 'finished')), false)::boolean as played_by_experience
  from experiences ex
  where ex.user_id = le.user_id and ex.media_item_id = le.media_item_id
) x
left join lateral (
  select a.provider from sync_events ev
  join connected_accounts a on a.id = ev.account_id
  where ev.library_entry_id = le.id and ev.user_id = le.user_id
  order by ev.id
  limit 1
) src on true
where le.user_id = @user_id and le.media_item_id = @media_item_id
order by le.edition_id nulls first, le.created_at;

-- name: LibraryEditionIDForItem :one
select id from editions where public_id = @public_id and media_item_id = @media_item_id;

-- name: LibraryInsertEntry :one
insert into library_entries (user_id, media_item_id, edition_id, ownership_user, backlog, acquired_on, tags, notes, visibility)
values (@user_id, @media_item_id, @edition_id, @ownership_user, @backlog, @acquired_on, @tags, @notes, @visibility)
returning *;

-- name: LibraryUpdateEntry :one
with u as (
  update library_entries
  set ownership_user = @ownership_user, backlog = @backlog, acquired_on = @acquired_on, tags = @tags,
      notes = @notes, visibility = @visibility, updated_at = now()
  where library_entries.public_id = @entry_id::uuid and library_entries.user_id = @user_id::bigint
  returning media_item_id
)
select m.public_id from u join media_items m on m.id = u.media_item_id;

-- name: LibrarySetPlayed :one
with u as (
  update library_entries
  set played_user = @played_user, updated_at = now()
  where library_entries.public_id = @entry_id::uuid and library_entries.user_id = @user_id::bigint
  returning media_item_id
)
select m.public_id from u join media_items m on m.id = u.media_item_id;

-- name: LibrarySetOwnership :one
with u as (
  update library_entries
  set ownership_user = @ownership_user, updated_at = now()
  where library_entries.public_id = @entry_id::uuid and library_entries.user_id = @user_id::bigint
  returning media_item_id
)
select m.public_id from u join media_items m on m.id = u.media_item_id;

-- name: LibraryDeleteEntry :one
with u as (
  delete from library_entries
  where library_entries.public_id = @entry_id::uuid and library_entries.user_id = @user_id::bigint
  returning media_item_id
)
select m.public_id from u join media_items m on m.id = u.media_item_id;

-- name: LibraryGetEntryForEdit :one
select le.*, m.public_id as item_public_id, m.title as item_title, m.kind as item_kind
from library_entries le
join media_items m on m.id = le.media_item_id
where le.public_id = @public_id and le.user_id = @user_id;
