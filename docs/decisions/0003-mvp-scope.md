# ADR 0003 — Trimmed MVP and release train

- **Status:** Accepted
- **Date:** 2026-11-04
- **Amends:** README "Suggested MVP" (which bundled Steam, RetroAchievements, the retro registry, and federation into the first release)

## Context

The RFC's MVP contained roughly a year of work. The goal is a usable prototype as fast as possible that friends can self-host and update regularly.

## Decision

Ship in small releases. Each release is usable on its own.

| Release | Theme | Contents |
| --- | --- | --- |
| **v0.1** | Personal tracker | Local accounts (admin-created + first-run setup); games, books, films, TV (show level); manual entry; Open Library, TMDB, IGDB metadata search; editions/platforms; library entries with ownership; experiences with progress; ratings and reviews; mixed-media lists; journal; dashboard; basic stats; local cover cache; JSON export; CSV import; Docker image + Compose; backup docs |
| **v0.2** | Steam | Connected accounts framework, encrypted credentials, sync runs + audit log, Steam OpenID linking, owned-game import, playtime, achievements, override UI and *Reset to source* |
| **v0.3** | Retro | Full platform registry (aliases, families, admin corrections), platform catalog browsing, RetroAchievements linking + sync, ambiguous-match review queue |
| **v0.4** | Sharing | Visibility enforcement on public pages, public/instance profiles at `/@username`, shareable lists and reviews, follows inside one instance |
| **v0.5** | Federation | ActivityPub actors, WebFinger, HTTP signatures, follow/accept, outbound delivery of explicitly public activity |
| Later | Breadth | Plex/Tautulli, Calibre, Audiobookshelf, Jellyfin, Trakt, music, tabletop, TV episode tracking, year-in-review, OIDC |

### What v0.1 keeps from the later releases (cheap now, expensive later)

- Every user-facing record has a `public_id uuid` (UUIDv7) that's separate from its bigint primary key. See ADR 0004.
- Every shareable record has a `visibility` column, defaulting to `private`.
- `library_entries` already has the ADR 0002 source/user column pairs.
- `external_ids` is the single home of provider identifiers.
- The provider adapter interface is defined so that v0.2 account adapters fit beside metadata adapters.

### Explicitly out of v0.1

Wishlist (only a `backlog` flag; a `wishlist` ownership value can be added later), OIDC, Kubernetes/Helm, OpenTelemetry, a JSON API for third parties, TV episode/season progress, mobile apps, email delivery (password resets are admin-driven), and any scraping.

## Consequences

The RFC's MVP acceptance test still applies to v0.1. The Steam, retro, and federation acceptance tests move to v0.2, v0.3, and v0.5.
