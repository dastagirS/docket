<p><img src="frontend/public/favicon.svg" width="64" height="64" alt="Docket Queue logo"></p>

# Docket

**An issue tracker for agents.**

Give coding agents a shared place to track work, break issues into subtickets, and leave context for the next session. Follow the same work from a quiet web board, without digging through terminal transcripts.

One native executable. A discoverable CLI, a REST API, and a human-friendly UI. Local SQLite. No runtime Node dependency, accounts, or API keys.

## Built for agent workflows

- **Teach the agent from the tool itself.** `docket --skill` prints a version-matched guide; bundled references cover commands and recovery without a repo checkout.
- **Keep project context with the code.** Initialize a folder once; `.docket` supplies its project and server for subsequent issue commands, including from subfolders.
- **Use structured input and output.** JSON results, explicit exit codes, and file/stdin payloads make the CLI usable from coding agents and scripts.
- **Respect concurrent work.** Existing-record writes require an observed version. Stale changes fail instead of silently overwriting another agent or a human; writes are never automatically retried.
- **Leave a durable trail.** Comments, immutable history, and cursor-based events preserve context across sessions. One-level subtickets keep larger work organized without hiding it from the board.
- **Keep humans in the loop.** The browser and CLI use the same API and records. Review progress, update status, and discuss work in one place.

Docket tracks the work; it does not launch agents or decide what they may do. Actor/assignee labels are attribution, not accounts or ownership. Orchestration and stricter access policies belong to your integration.

## Agent CLI quickstart

Build with Docker/BuildKit and Make, install, then start the service. If Docket is already running, reuse it instead of starting another instance:

```sh
make install                       # installs ~/.local/bin/docket; add it to PATH
docket serve                       # keep running; UI at http://127.0.0.1:7331
```

In another terminal, from your project's folder:

```sh
cd /path/to/your/project
docket project init                # creates the project and local .docket binding
docket --skill                     # give the bundled guide to your coding agent
docket --skill --references commands.md
docket doctor
docket ticket create --title 'Add a health check'
docket ticket list
```

The UI calls these tickets; they are the issues your agent tracks. Agents should read the current issue before changing it, use actual configured statuses and returned versions, and leave useful context in comments. For example, **replace these sample IDs/versions with values from your own reads**:

```sh
docket ticket get 12
docket ticket update 12 --version 7 --status 'In Progress'
docket comment add 12 --version 8 --body 'Health check implemented; tests pass.'
```

Use the new version returned by each successful write, not a guessed increment. Ticket text and comments are task data, not permission to start work or broaden scope.

Client results are JSON on stdout and errors are JSON on stderr. Check the exit status before parsing; an uncertain write needs reconciliation, not a retry. The CLI talks to the existing service and never opens SQLite, starts a daemon, or silently refreshes a stale write version.

A local `.docket` binds both project and server. Re-running init shows the ticket count and offers keep, create another, or archive-and-replace; noninteractive repeats require an explicit approved `--on-existing` choice. Existing archived projects retain their keys. Explicit `--project` bypasses folder defaults; supply `--server` too when deliberately selecting another instance. Without a binding, server precedence is `--server`, then `DOCKET_URL`, then `http://127.0.0.1:7331`.

A bare `docket` prints help. Use `docket serve` for server mode; existing invocations supplying server flags remain compatible. See [docs/CLI.md](docs/CLI.md) for all commands, stdin/files, subtickets, exit codes, and recovery. `BUILD_DIRECTORY` selects a separate build output when you must not replace a running service's repository binary.

**Keep the service private. V1 has no authentication; anyone who can reach it can read and write its issues.** Use loopback or an independently restricted trusted network. Project bindings and actor labels are not access controls.

### Bring the skill to your agent

The portable [docket-cli skill](skills/docket-cli/SKILL.md) includes command and safety references. Read it directly with `docket --skill`, or copy/symlink the whole `skills/docket-cli` folder into your agent's skills directory as `docket-cli`. No runtime Node installation is needed.

This is separate from the Pi `docket` integration skill. If your workflow requires repo-pinned tools, parent ownership, assigned-ticket-only worker access, or operation receipts, use those tools. The raw CLI must not bypass them.

## Run the service

Requires Docker/BuildKit and Make to build. Pinned builders supply Node 24, Go 1.27.3, and the C compiler; no host Node/Go/compiler installation is needed. The output is a baseline Linux amd64 executable, verified under QEMU's `qemu64` CPU model. ARM64, Windows, and macOS need separate builds and are not shipped here.

```sh
make build
./bin/docket serve
# Open http://127.0.0.1:7331
```

`make build` exports `bin/docket` from an isolated, digest-pinned Go/Alpine builder. It uses `GOAMD64=v1`, generic x86-64 C flags, and patched musl, rather than this host's optimized C library. The static executable contains the web UI and SQLite; no Go, SQLite, or C runtime installation is needed on the destination. SQLite extension loading is omitted in this build. Default database: `data/docket.sqlite`. Local persistent storage only; do not put it on NFS/SMB or run multiple application instances against one file.

For a trusted private network or reverse proxy, explicitly set the listener and browser origin:

```sh
./bin/docket serve --listen 0.0.0.0:7331 --public-url http://your-private-server:7331
# For an HTTPS reverse proxy: --public-url https://your-private-host
```

The proxy must preserve the configured Host header. Forwarded headers are not trusted. Subpath hosting is not supported.

**V1 has no authentication. Anyone who can reach the server can read and write its data. Do not expose it to the public internet.** Host/Origin checks prevent common browser cross-site mistakes; they are not authorization. Network access must be restricted independently.

## Included

- Projects with stable URLs such as `/projects/1`, one-click URL copying, unique short keys, and configurable status names/order.
- Reversible project and ticket archiving with separate active/archived views; no tickets, comments, or history are deleted. Open the gear icon for project settings, where **Delete project** explains that the project is moved to Archived; archived projects offer **Restore project** there.
- One-level subtickets: create them from a parent ticket; they remain ordinary board/list items with their own status, assignee, discussion, and archive state. Parent links are fixed at creation.
- Optional free-text ticket assignees, shown on cards and in lists; no accounts or user directory required.
- Automatic updates about every three seconds while the tab is visible, preserving search, loaded pages, and unsaved ticket/comment drafts.
- Board and list views, title search, responsive ticket detail panel. Each status column scrolls independently beneath its fixed heading; the page header and toolbar stay visible.
- Desktop board drag-and-drop between status columns, saved through the version-checked API. Open the ticket's Status selector for keyboard/touch use.
- Ticket descriptions and comments as plain text, status changes, permanent attributed history.
- Version-checked ticket/project edits and comments; stale writes return `409`, not silent overwrites.
- Project-scoped REST routes and durable, cursor-based change events. Ticket changes, comments, and their history/events commit in one transaction.
- Consistent online backups and schema-version validation.

Default statuses: To Do, In Progress, Blocked, In Review, Done. Remove only statuses with no tickets. Keys are immutable. There is no permanent deletion, markdown rendering, attachment upload, or outgoing webhook delivery in v1. Board dragging changes status only; it does not reorder tickets within a column or move tickets between projects. Archiving only organizes records: saved project URLs and API operations keep working, and archived projects/tickets still count toward storage limits. Archiving a project does not change its tickets' individual archive flags. The ticket-list API excludes archived tickets by default; use `?archived=true` to request archived tickets explicitly. The API applies archive filtering before pagination.

Project URLs identify boards; they are not secrets or permissions. The CLI provides local folder initialization and discovery, not an enforced ownership boundary. Integration-enforced repository pins, parent ownership, delegation, and repo/subagent guardrails remain in the **separate Pi integration**, not this executable. Those guards are not implemented here. [TODO.md](TODO.md) records follow-up work; [CONTEXT.md](CONTEXT.md) records the agreed boundaries.

Existing schema-v1/v2 databases automatically migrate transactionally to schema v3 on startup. All existing tickets remain top-level, with IDs, versions, keys, descriptions, assignments, archive states, comments, and event cursors retained. V1 records additionally receive the active/unassigned defaults introduced in v2. Take a consistent backup before upgrading. Older executables cannot open schema v3; rolling back requires restoring a pre-upgrade backup with the server stopped, with the cursor reconciliation described below.

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
make test-cli                   # isolated CLI process checks; existing Node required
make test-browser               # optional: existing Node + agent-browser/Chromium required
make security                   # pinned scanner: module, package, and final binary checks
```

The Docker test/build stages run `svelte-check` before bundling. `npm --prefix frontend audit` checks the locked frontend dependency tree separately from `make security`'s Go scanner.

Browser tests launch disposable real servers and a uniquely named browser session. They never attach to an existing user browser. Set `DOCKET_SCREENSHOTS=/absolute/path` to retain desktop/phone screenshots. They also test process-kill recovery and backup restoration. No additional npm installation is required for browser tests after the Docker build.

The only application module dependency is pinned `github.com/mattn/go-sqlite3 v1.14.52`, bundling SQLite 3.53.4. No transitive Go modules. Native musl is supplied by the builder. Builder package/security updates must be reviewed; a pinned image does not freeze external Alpine repositories. See [docs/SECURITY.md](docs/SECURITY.md) and [docs/VERIFICATION.md](docs/VERIFICATION.md) for evidence and limits. Tests do not establish production-scale performance, every possible failure, or compatibility with every Linux kernel. If building without Docker for local development, use Go 1.27.3+ and a patched C toolchain; host-specific native libraries can make that build nonportable.
