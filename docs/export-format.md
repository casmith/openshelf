# Export format: `openshelf.export.v1`

`GET /settings/export.json` (linked from **Settings → Export**) streams a JSON document with everything the signed-in user owns. It never includes another user's data. The file can be imported back on **Settings → Import**, on the same instance or another one.

## Top level

```json
{
  "format": "openshelf.export.v1",
  "exported_at": "2026-11-04T12:00:00Z",
  "user": { "username": "alice", "display_name": "Alice" },
  "items": [],
  "editions": [],
  "library_entries": [],
  "experiences": [],
  "reviews": [],
  "lists": [],
  "activity": []
}
```

Every array is present, even when empty. `id` values are the record's public UUIDs. References between records use those IDs. Optional fields are left out when unset. Dates are `YYYY-MM-DD`, and timestamps are RFC 3339.

## Catalog (embedded so the file stands alone)

`items`: every catalog item the user's records refer to.

| Field | Notes |
| --- | --- |
| `id` | Item public ID |
| `kind` | `game`, `book`, `film`, `show` |
| `title`, `original_title` | |
| `year`, `release_date` | |
| `external_ids` | `[{"provider": "igdb_game", "value": "1234"}]`, using the namespaced providers in `external_ids` (`isbn13`, `tmdb_movie`, `tmdb_tv`, `igdb_game`, ...) |

`editions`: editions referenced by entries or experiences: `id`, `item`, `title`, `format`, `platform` (platform slug, e.g. `dreamcast`), `region`, `publisher`, `release_date`.

## Personal records

| Section | Fields |
| --- | --- |
| `library_entries` | `id`, `item`, `edition`, `ownership` (the user's value: `owned`, `borrowed`, `not_owned`), `backlog`, `played` (explicit user value), `acquired_on`, `tags`, `notes`, `visibility`, `created_at`, `updated_at` |
| `experiences` | `id`, `item`, `edition`, `state` (`active`, `paused`, `finished`, `abandoned`), `started_on`, `finished_on`, `progress_unit`, `progress_current`, `progress_total`, `notes`, `visibility`, `created_at`, `updated_at`, `progress_events` (`[{occurred_at, value, note}]`) |
| `reviews` | `id`, `item`, `experience`, `rating` (0–100), `body`, `contains_spoilers`, `visibility`, `created_at`, `updated_at` |
| `lists` | `id`, `name`, `description`, `ranked`, `visibility`, `created_at`, `updated_at`, `items` (`[{item, position, note}]`) |
| `activity` | `id`, `verb`, `item`, `library_entry`, `experience`, `review`, `list`, `visibility`, `occurred_at` |

Source-owned values (`ownership_source`, `played_source`, sync data) aren't exported.

## Importing a JSON export

The import runs as a background job. You get a dry-run preview first, then confirm.

- **Items** are matched by public ID (same instance), then by any external ID, then by exact title + year + kind (case-insensitive). If nothing matches, the importer creates a manual item and records its external IDs.
- **Editions** are matched by public ID, then by item + platform + format + title. Otherwise one is created.
- **Records** are matched by their natural keys, so re-importing the same file creates nothing:
  - entries: item + edition
  - experiences: item + edition + state + dates
  - reviews: one per item
  - lists: by name, with list items by item
  - progress events: timestamp + value
  - activity: verb + item + time + referenced record
- An existing entry is merged with the import. Set values overwrite, and tags are unioned. An existing review is updated to the imported rating and body.
- Imported records are always **private**, whatever their exported visibility. New record IDs are generated, and `created_at` / `updated_at` aren't preserved. Journal events keep their original `occurred_at`.
- A record that can't be imported (for example, an unknown platform slug) is reported by its position, such as `experiences[3]`, and skipped. The rest of the file still imports.

Preview counts are per record (journal events aren't counted): **create** means a new catalog item was needed, **match** means new data was written for an existing item, and **skip** means it was already there.

Uploads are limited to 10 MB.
