# Backup and restore

An OpenShelf instance has two pieces of state:

- the **Postgres database** (volume `pgdata`): users, catalog, library, journal, reviews, lists, sessions, and queued jobs;
- the **data volume** (volume `data`, mounted at `/data`): cached cover images and other files the app writes.

The scripts in `scripts/` back up both through the compose stack, so they work while the app is running.

## Back up

From the repository root (or anywhere, with `COMPOSE_FILE` pointing at your `compose.yaml`):

```sh
scripts/backup.sh                       # writes ./backups/openshelf-<UTC timestamp>/
scripts/backup.sh /mnt/nas/openshelf    # or choose the directory
```

Each backup directory contains:

| File | Made with |
| --- | --- |
| `db.dump` | `pg_dump -Fc` run inside the `postgres` container (compressed, consistent snapshot) |
| `data.tar.gz` | `tar` of the data volume, run in a throwaway `toolbox` container |

Copy the directory off the host. A nightly cron job is enough for most instances:

```
15 3 * * * cd /opt/openshelf && scripts/backup.sh /backups/openshelf-$(date +\%F) >/dev/null
```

Treat backups as sensitive: they contain password hashes and, from v0.2, encrypted provider credentials. Restoring those credentials needs the same `OPENSHELF_SECRET_KEY`, so store the key (separately) with your backups.

If you only use the plain compose file without the scripts, the equivalent commands are:

```sh
docker compose exec -T postgres pg_dump -U openshelf -d openshelf -Fc > db.dump
docker compose run --rm --no-deps -T toolbox tar -C /data -czf - . > data.tar.gz
```

## Restore

Restoring **replaces** the current database and data volume.

```sh
scripts/restore.sh backups/openshelf-20261104T031500Z
```

The script:

1. stops `app`, and starts `postgres` if needed;
2. drops and recreates the `openshelf` database, then runs `pg_restore` from `db.dump`;
3. empties the data volume and extracts `data.tar.gz` into it, preserving file ownership;
4. starts `app` and waits for it to be healthy. Any newer migrations apply on startup.

Pass `-y` to skip the confirmation prompt, and `--no-start` to leave the app stopped.

To move an instance to a new host: install the stack there (see [self-hosting.md](self-hosting.md)) with the same `OPENSHELF_SECRET_KEY`, run `docker compose up -d postgres`, then `scripts/restore.sh -y <backup-dir>`.

A backup can be restored into the same or a newer OpenShelf version, not an older one, because migrations are forward-only.

## Verification

`scripts/backup-restore-test.sh` runs in CI on every pull request. It builds the image, starts a throwaway stack (project `openshelf-brtest`, port 18080), runs `openshelf seed`, writes a marker file to the data volume, and records the row count of every table. Then it backs up, deletes the containers and volumes, restores, and checks that the row counts match exactly, the marker file is back with its owner intact, the app is ready, and a seeded user can sign in. River's job tables are excluded from the comparison because the running app updates them.

Run it locally with `scripts/backup-restore-test.sh` (it needs Docker with Compose v2.24 or newer). It removes everything it created when it exits.
