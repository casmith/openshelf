# ADR 0004 — Public identifiers, URLs, and visibility

- **Status:** Accepted
- **Date:** 2026-11-04

## Decision

1. **Primary keys** are `bigint generated always as identity`. They are never exposed in URLs, HTML, exports, or federation.
2. **Public IDs** are `uuid` columns defaulting to Postgres 18's `uuidv7()`, unique, on every row that appears in a URL or export: users, media_items, editions, library_entries, experiences, reviews, lists, activity_events.
3. **Stable URLs** (federation-ready from day one):
   - Users: `/@{username}`. Usernames are immutable after creation in v0.1.
   - Items: `/items/{public_id}`
   - Lists: `/lists/{public_id}`; reviews: `/reviews/{public_id}`; activity: `/activity/{public_id}`
4. **Visibility** is a Postgres enum `visibility` with values `private`, `instance` (any logged-in user on this server), and `public`. Every shareable table has `visibility visibility not null default 'private'`. Users have a `default_visibility` that new records copy.
5. **Authorization is enforced in SQL.** Every query that reads user-owned rows takes the viewer's user ID and filters by owner or visibility. Handlers never fetch a row first and check ownership afterward. Each slice includes a cross-user isolation test.
6. Imported records (v0.2+) are always created `private`, regardless of `default_visibility`.
