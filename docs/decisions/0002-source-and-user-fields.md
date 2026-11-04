# ADR 0002 — Paired source/user columns instead of a generic FieldOverride table

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

The RFC requires that imported observations (Steam playtime, RetroAchievements unlocks, provider metadata) never overwrite a user's explicit choices. It proposed a generic `FieldOverride` entity holding one row per overridden field.

A generic override table makes the most common queries, like "filter my library by played/owned", depend on joins plus `COALESCE` across rows. It also can't be indexed sensibly and is hard to test.

## Decision

Only a few fields need provenance. For each one, store **both** values as typed columns on the owning row:

```sql
played_source  boolean,      -- written only by sync/import code
played_user    boolean,      -- written only by explicit user action; NULL = no override
-- effective value: COALESCE(played_user, played_source)
```

- Sync code writes **only** `*_source` columns. A unit test asserts that no sync code path writes `*_user` columns.
- User actions write **only** `*_user` columns.
- *Reset to source* sets `*_user = NULL`.
- Effective values are computed in SQL (`COALESCE`) or by a generated column where indexing matters.
- Every sync-originated change is recorded in `sync_events` (v0.2) with `respected_override = true/false`, which powers the sync history screen.

Fields that are **always user-owned** (rating, review, notes, lists, experiences, backlog, visibility) have no `*_source` column at all. Sync code has no way to write them.

Fields that are **always source-owned** (playtime, last played, achievement unlocks) live in source-scoped tables keyed by connected account (`external_playtime`, `achievement_unlocks`). They are never displayed as personal experiences.

Catalog metadata corrections (titles, platform names, release dates) use the same pattern only where needed (`title_source` / `title_override`). v0.1 has only manual and single-provider metadata, so v0.1 ships just `media_items.locked_fields text[]`: fields an admin edited by hand are skipped on provider refresh.

### Fields using the pattern (initial list)

| Table | Pair | Introduced |
| --- | --- | --- |
| `library_entries` | `ownership_source` / `ownership_user` | v0.1 (columns exist; source unused until v0.2) |
| `library_entries` | `played_source` / `played_user` | v0.1 (columns exist; source unused until v0.2) |
| `media_items` | `locked_fields` | v0.1 |

## Consequences

- Library filters are plain indexed `WHERE` clauses.
- The RFC's Portal 2 acceptance test becomes a short, direct test: sync sets `played_source = true`, the user sets `played_user = false`, the next sync updates `played_source` and playtime, and `COALESCE` still yields `false`.
- Adding a new overridable field takes a migration (two columns) rather than a data convention. That's acceptable: overridable fields should be rare and deliberate.
