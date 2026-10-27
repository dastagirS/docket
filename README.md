# Docket

Small, standalone ticket tracking. One Go executable, local SQLite, and a quiet, Kaneo-inspired dark interface built with Svelte and TypeScript through Vite. No accounts or API keys. Node is a build/development tool, not a runtime requirement.

## Run

Requires Docker/BuildKit and Make to build. Pinned builders supply Node 24, Go 1.27.3, and the C compiler; no host Node/Go/compiler installation is needed. The output is a baseline Linux amd64 executable, verified under QEMU's `qemu64` CPU model. ARM64, Windows, and macOS need separate builds and are not shipped here.

```sh
make build
./bin/docket
# Open http://127.0.0.1:7331
```

`make build` exports `bin/docket` from an isolated, digest-pinned Go/Alpine builder. It uses `GOAMD64=v1`, generic x86-64 C flags, and patched musl, rather than this host's optimized C library. The static executable contains the web UI and SQLite; no Go, SQLite, or C runtime installation is needed on the destination. SQLite extension loading is omitted in this build. Default database: `data/docket.sqlite`. Local persistent storage only; do not put it on NFS/SMB or run multiple application instances against one file.

For a trusted private network or reverse proxy, explicitly set the listener and browser origin:

```sh
./bin/docket --listen 0.0.0.0:7331 --public-url http://your-private-server:7331
# For an HTTPS reverse proxy: --public-url https://your-private-host
```

The proxy must preserve the configured Host header. Forwarded headers are not trusted. Subpath hosting is not supported.

**V1 has no authentication. Anyone who can reach the server can read and write its data. Do not expose it to the public internet.** Host/Origin checks prevent common browser cross-site mistakes; they are not authorization. Network access must be restricted independently.

## Included

- Projects with stable URLs such as `/projects/1`, one-click URL copying, unique short keys, and configurable status names/order.
- Reversible project and ticket archiving with separate active/archived views; no tickets, comments, or history are deleted. Open the gear icon for project settings, where **Delete project** explains that the project is moved to Archived; archived projects offer **Restore project** there.
- Optional free-text ticket assignees, shown on cards and in lists; no accounts or user directory required.
- Automatic updates about every three seconds while the tab is visible, preserving search, loaded pages, and unsaved ticket/comment drafts.
- Board and list views, title search, responsive ticket detail panel.
- Desktop board drag-and-drop between status columns, saved through the version-checked API. Open the ticket's Status selector for keyboard/touch use.
- Ticket descriptions and comments as plain text, status changes, permanent attributed history.
- Version-checked ticket/project edits and comments; stale writes return `409`, not silent overwrites.
- Project-scoped REST routes and durable, cursor-based change events. Ticket changes, comments, and their history/events commit in one transaction.
- Consistent online backups and schema-version validation.

Default statuses: To Do, In Progress, Blocked, In Review, Done. Remove only statuses with no tickets. Keys are immutable. There is no permanent deletion, markdown rendering, attachment upload, or outgoing webhook delivery in v1. Board dragging changes status only; it does not reorder tickets within a column or move tickets between projects. Archiving only organizes records: saved project URLs and API operations keep working, and archived projects/tickets still count toward storage limits. Archiving a project does not change its tickets' individual archive flags. The ticket-list API excludes archived tickets by default; use `?archived=true` to request archived tickets explicitly. The API applies archive filtering before pagination.

Project URLs identify boards; they are not secrets or permissions. Repository initialization, discovery, parent ownership, delegation, and repo/subagent guardrails remain in the **separate Pi integration**, not this executable. Those guards are not implemented here. [TODO.md](TODO.md) records follow-up work; [CONTEXT.md](CONTEXT.md) records the agreed boundaries.

Existing schema-v1 databases automatically migrate to schema v2 on startup. Existing projects and tickets start active, and existing tickets start unassigned; IDs, keys, descriptions, comments, and event cursors are retained. Take a consistent backup before upgrading. Older executables cannot open schema v2; rolling back requires restoring a pre-upgrade backup with the server stopped, with the cursor reconciliation described below.

Live updates use bounded polling of the durable event feed, not WebSockets or a permanent connection. Hidden tabs pause polling. Open tickets update automatically unless there are unsaved ticket edits; those edits are kept and a reload/conflict warning is shown instead. An unsent comment is preserved. Each visible tab consumes requests under the instance-wide rate limit, and the UI's existing record-display limits still apply. Polling pauses during a board drag or pending drop so the ticket's original version cannot be silently refreshed mid-gesture. Drops use that version in `If-Match`; failures show an error and reconcile with the API rather than leaving a client-only move. Archived tickets are not draggable, and a same-column or cancelled drag does not write history. Native desktop dragging is verified in Chromium; touch dragging and other browser engines are not certified.

## Frontend development

Frontend source lives in `frontend/src/`: Svelte components, strict TypeScript API parsing, and CSS. `frontend/public/` contains the SVG icons and locally served Manrope font (with its OFL license). No external font/CDN requests are made by the app.

For hot reload, install Node 24+ and run:

```sh
npm --prefix frontend ci
# Keep the Go backend running on http://127.0.0.1:7331 in another terminal.
make frontend-dev
# Open http://127.0.0.1:5173 (including /projects/1 deep links).
```

The development server proxies `/api` to the local Go backend and rewrites Host/Origin only for that local proxy. Do not expose the development server publicly. The production build uses the unchanged Go Host/Origin checks and CSP.

```sh
make frontend-build             # local type check + Vite build
make build                      # Docker builds frontend, then embeds it in Go
```

`internal/docket/web/` is generated output, not source; do not edit it. Docker builds it from the lockfile even when that directory is absent. For a direct host Go build, run `make frontend-build` first. The final executable still needs neither Node nor a separate frontend server.

## Docker

The Dockerfile builds the binary in the same pinned builder. The runtime image uses `scratch`, runs as UID/GID 65532, and initializes a named volume with that user's ownership.

```sh
docker build --platform linux/amd64 -t docket:local .
docker volume create docket-data
docker run --rm --name docket -p 127.0.0.1:7331:7331 \
  -v docket-data:/data docket:local
```

Open `http://127.0.0.1:7331`. If you change the external host/port, also override `--public-url`. Existing bind-mounted directories need write permission for UID/GID 65532. Do not use a shared-network volume. No other service or database container is needed.

## Backup and restore

```sh
# Works while the server is running; destination must not exist.
./bin/docket --database data/docket.sqlite --backup-to /safe/local/path/backup.sqlite
```

The parent directory must already exist. `VACUUM INTO` takes a consistent SQLite snapshot including committed WAL data. To restore, stop Docket, retain the old database and any `-wal`/`-shm` files together elsewhere, and place the backup at the configured database path before restarting. Never overwrite a live database or leave old WAL files beside a restored database. Restore the entire database, not selected tables; event IDs are consumer cursors. After rolling back to an older backup, integrations must reconcile/reseed their saved cursors rather than assume newer changes survived.

## API and limits

See [docs/API.md](docs/API.md) for request examples, versions, events, and limits. Limits fail explicitly; history/events are never silently evicted. There is no automatic retention job. Back up and monitor disk use. Increasing a limit requires an intentional reviewed code change.

## Verify

```sh
make test                       # isolated Go tests with race detector + vet
make verify-portable            # SQLite backup + API tests under baseline CPU emulation
make test-browser               # optional: existing Node + agent-browser/Chromium required
make security                   # pinned scanner: module, package, and final binary checks
```

The Docker test/build stages run `svelte-check` before bundling. `npm --prefix frontend audit` checks the locked frontend dependency tree separately from `make security`'s Go scanner.

Browser tests launch disposable real servers and a uniquely named browser session. They never attach to an existing user browser. Set `DOCKET_SCREENSHOTS=/absolute/path` to retain desktop/phone screenshots. They also test process-kill recovery and backup restoration. No additional npm installation is required for browser tests after the Docker build.

The only application module dependency is pinned `github.com/mattn/go-sqlite3 v1.14.52`, bundling SQLite 3.53.4. No transitive Go modules. Native musl is supplied by the builder. Builder package/security updates must be reviewed; a pinned image does not freeze external Alpine repositories. See [docs/SECURITY.md](docs/SECURITY.md) and [docs/VERIFICATION.md](docs/VERIFICATION.md) for evidence and limits. Tests do not establish production-scale performance, every possible failure, or compatibility with every Linux kernel. If building without Docker for local development, use Go 1.27.3+ and a patched C toolchain; host-specific native libraries can make that build nonportable.
