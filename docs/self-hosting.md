# Self-hosting OpenShelf

OpenShelf runs as one container next to PostgreSQL 18. At idle it uses well under 60 MB of RAM and close to 0% CPU, so it runs fine on a Raspberry Pi class machine. Images are published for `linux/amd64` and `linux/arm64`.

## Quickstart

With a clone of the repository:

```sh
git clone https://github.com/casmith/openshelf.git
cd openshelf/deploy
cp ../.env.example .env
$EDITOR .env           # set POSTGRES_PASSWORD, OPENSHELF_SECRET_KEY, OPENSHELF_BASE_URL
docker compose up -d
```

Without a clone, download the two files into an empty directory:

```sh
curl -fsSLO https://raw.githubusercontent.com/casmith/openshelf/master/deploy/compose.yaml
curl -fsSL -o .env https://raw.githubusercontent.com/casmith/openshelf/master/.env.example
sed -i '/^    build:/d' compose.yaml   # nothing to build without the source
$EDITOR .env
docker compose up -d
```

Generate the secrets with `openssl rand -hex 24` (Postgres password) and `openssl rand -hex 32` (secret key). The app refuses to start in production when `OPENSHELF_SECRET_KEY` is shorter than 32 characters. Keep the Postgres password URL-safe (hex is), because it's embedded in the database URL.

Then open `http://<host>:8080/setup` and create the first admin account. `/setup` is only available until the first account exists. You can also create users from the command line:

```sh
docker compose exec app /openshelf user create --admin --username alice
```

`docker compose ps` shows both services as `healthy` once the app answers `/readyz` (the app's healthcheck runs `openshelf healthcheck` inside the container, because the distroless image has no shell or curl).

## What's in the stack

| Service | Image | Data |
| --- | --- | --- |
| `app` | `ghcr.io/casmith/openshelf:latest` (override with `OPENSHELF_IMAGE`) | volume `data` at `/data` (cached cover images) |
| `postgres` | `postgres:18` | volume `pgdata` at `/var/lib/postgresql` |
| `toolbox` | `postgres:18`, profile `tools` | used only by the backup and restore scripts |

The app container runs as a non-root user with a read-only root filesystem and no Linux capabilities. Back up both volumes; see [backup-restore.md](backup-restore.md).

## Configuration

All configuration is environment variables. `.env.example` lists every one with a comment. Compose reads `deploy/.env`, but you can inject the same variables any other way (Infisical, Docker secrets turned into env, external-secrets in Kubernetes, ...). The app only sees the environment.

| Variable | Default | Notes |
| --- | --- | --- |
| `OPENSHELF_SECRET_KEY` | none | Required in production, 32+ characters. Encrypts stored account credentials (Steam and other connections, from v0.2); changing it makes them unreadable. |
| `OPENSHELF_BASE_URL` | `http://localhost:8080` | The public URL. Used for links and to decide whether cookies are `Secure`. |
| `OPENSHELF_TRUSTED_PROXIES` | _(empty)_ | Comma-separated CIDRs or IPs of your reverse proxy. `X-Forwarded-For` from these addresses is used to find the real client IP for login rate limiting and logs. Leave empty when nothing sits in front of OpenShelf. |
| `OPENSHELF_DATABASE_URL` | built from `POSTGRES_PASSWORD` | Set it to use an external Postgres 18 server. |
| `OPENSHELF_ENV` | `production` in the image | |
| `OPENSHELF_AUTO_MIGRATE` | `true` | Applies migrations on startup. |
| `OPENSHELF_TMDB_TOKEN`, `OPENSHELF_IGDB_CLIENT_ID`, `OPENSHELF_IGDB_CLIENT_SECRET`, `OPENSHELF_STEAM_API_KEY` | empty | Metadata providers, see below. |
| `OPENSHELF_HTTP_PORT` | `8080` | Compose only: host port, or `127.0.0.1:8080` to listen on loopback only. |

## Behind a reverse proxy

Run OpenShelf behind a TLS-terminating proxy (Caddy, Traefik, nginx) for anything reachable from outside your LAN:

1. Set `OPENSHELF_TRUSTED_PROXIES` to your proxy's address or network (e.g. `172.18.0.0/16` for a Docker network, or the pod CIDR in Kubernetes). Without it, every request appears to come from the proxy and one user's failed logins can rate-limit everyone.
1. Set `OPENSHELF_BASE_URL=https://shelf.example.com`. An `https://` base URL makes session cookies `Secure`, so logins only work over HTTPS from then on.
2. Bind the app to loopback (`OPENSHELF_HTTP_PORT=127.0.0.1:8080`) or put the proxy on the compose network and drop the `ports:` mapping.
3. Forward the original `Host` header. Cross-origin form posts are rejected, so the browser's `Origin` must match the host it sees.

Caddy example:

```
shelf.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

nginx needs `proxy_set_header Host $host;` and `proxy_set_header X-Forwarded-Proto $scheme;` in the `location` block.

The app sends its own `Content-Security-Policy`, `X-Frame-Options`, and related headers, so the proxy doesn't need to add them. Add `Strict-Transport-Security` at the proxy once HTTPS works.

## Metadata providers

Open Library (books) works without a key. The others are optional; without a key, that provider is hidden and you can still add items by hand.

- **TMDB** (films and TV): create an account, then copy the *API Read Access Token* from <https://www.themoviedb.org/settings/api> into `OPENSHELF_TMDB_TOKEN`.
- **IGDB** (games): register an application at <https://dev.twitch.tv/console/apps> (OAuth redirect `http://localhost` is fine), then set `OPENSHELF_IGDB_CLIENT_ID` and `OPENSHELF_IGDB_CLIENT_SECRET`.
- **Steam** (library sync, from v0.2): get a key at <https://steamcommunity.com/dev/apikey> and set `OPENSHELF_STEAM_API_KEY`.

Restart the app after changing keys: `docker compose up -d`.

## Installing as an app

OpenShelf is a Progressive Web App. Over HTTPS (or on `localhost`), browsers offer *Install* or *Add to Home Screen*. The service worker only caches static assets and an offline page; your library and pages always come from the server.

## Upgrading

```sh
./scripts/backup.sh                 # optional, but cheap
docker compose pull
docker compose up -d
```

Migrations run automatically on startup (unless `OPENSHELF_AUTO_MIGRATE=false`, in which case run `docker compose run --rm app migrate` first). Migrations are forward-only, so to roll back to an older version, restore the backup you took before upgrading.

Pin a version with `OPENSHELF_IMAGE=ghcr.io/casmith/openshelf:0.1` (minor line) or `:0.1.0` (exact) if you'd rather upgrade deliberately than track `latest`.

## Troubleshooting

- `docker compose logs app` shows JSON logs. Set `OPENSHELF_DEBUG=1` for more detail.
- `set POSTGRES_PASSWORD in .env`: compose can't find `.env` next to `compose.yaml`.
- `OPENSHELF_SECRET_KEY must be at least 32 characters`: generate one with `openssl rand -hex 32`.
- Logged out immediately after signing in: `OPENSHELF_BASE_URL` is `https://` but you're browsing over plain HTTP.
