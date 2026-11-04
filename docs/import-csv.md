# CSV import

Upload a CSV on **Settings → Import** (`/settings/import`). The file is checked in a dry run first, and the preview shows how many rows will **create** a new catalog item, **match** an existing one, or be **skipped** because they're already imported. Rows with problems are listed by line number. Confirm to run the import. Uploads are limited to 10 MB.

## Columns

The first row is a header. Column order doesn't matter, names are case-insensitive, and unknown columns are ignored. Only `kind` and `title` are required.

```
kind,title,year,isbn13,tmdb_id,igdb_id,platform,ownership,backlog,status,started_on,finished_on,rating_100,review,tags
```

| Column | Values |
| --- | --- |
| `kind` | `game`, `book`, `film` (or `movie`), `show` (or `tv`) |
| `title` | Item title |
| `year` | Four-digit release year |
| `isbn13` | Books only. Hyphens and spaces are ignored |
| `tmdb_id` | Films (`tmdb_movie`) and shows (`tmdb_tv`) |
| `igdb_id` | Games (`igdb_game`) |
| `platform` | Games only. A platform name, short name, slug, or alias (`Dreamcast`, `ps2`, `Mega Drive`) |
| `ownership` | `owned`, `borrowed`, `not_owned`. Empty leaves it unset |
| `backlog` | `true`/`false` (also `yes`/`no`, `1`/`0`) |
| `status` | `in_progress`, `finished`, `abandoned`. Empty means no play record unless dates are given: `finished_on` implies finished, and `started_on` alone implies in progress |
| `started_on`, `finished_on` | `YYYY-MM-DD` |
| `rating_100` | Whole number 0–100 |
| `review` | Review text |
| `tags` | Separated by `;` (or `,` inside a quoted cell). Stored lowercase |

An ID column only applies to its kinds. For example, `isbn13` is ignored on a game row.

## What a row does

1. **Find the item.** The importer matches by external ID first. If there's no match, it looks for an exact case-insensitive title + kind match on the same year (any year when `year` is empty). If nothing matches, it creates a manual item owned by you and records the IDs from the row.
2. **Platform.** For a game with a `platform`, it uses that item's edition on the platform, creating the edition if needed.
3. **Library entry.** It creates the entry, or merges into an existing one: set values overwrite, and tags are added.
4. **Play record.** When there's a status, it adds an experience unless one with the same state and dates already exists.
5. **Review.** When there's a rating or review text, it creates or updates your review of the item.

Everything is created **private**. Journal events (`added`, `started`, `finished`/`abandoned`, `rated`/`reviewed`) are recorded for new records, dated by `started_on` / `finished_on` when given.

Importing the same file again creates nothing new: every row is reported as skipped.

## Errors

A bad row is reported with its line number and skipped, and the rest of the file still imports. Examples: an unknown kind, an invalid date, a rating over 100, an unknown platform, or `finished_on` with `status=in_progress`. A file with no header, or without `kind` and `title` columns, fails as a whole.

## Example

```csv
kind,title,year,isbn13,tmdb_id,igdb_id,platform,ownership,backlog,status,started_on,finished_on,rating_100,review,tags
game,Shenmue,1999,,,1234,Dreamcast,owned,,finished,2024-01-02,2024-02-03,90,Forklift simulator.,retro;sega
book,Dune,1965,9780441013593,,,,owned,,in_progress,2024-05-01,,,,scifi
film,Arrival,2016,,329865,,,,true,,,,,,
```
