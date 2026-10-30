# Docket CLI

The same native executable provides a short-lived API client and an explicit `serve` command. Client commands never open SQLite, migrate a database, or automatically start/stop a service. The browser UI and CLI share the existing HTTP API and history/version rules.

## Build and install

```sh
make install
# Installs to ~/.local/bin/docket; ensure ~/.local/bin is on PATH.
docket --help
docket --skill
docket --skill --references
docket --skill --references commands.md
```

`PREFIX` changes the install prefix. `BUILD_DIRECTORY` changes the build output directory (default `bin`). Neither installation nor building restarts a systemd service. Installing a CLI in `~/.local/bin` does not replace a service executable configured at a different path.

A bare `docket` prints help. To run the server, use `docket serve`; older invocations beginning with `--listen`, `--database`, `--public-url`, or `--backup-to` (including single-dash forms) remain supported. **No-argument server startup has intentionally changed to help.** Existing systemd/Docker commands supplying server flags remain compatible.

## Connection and output

```sh
export DOCKET_URL=http://127.0.0.1:7331
export DOCKET_ACTOR='Build agent'  # optional attribution label, not authentication

docket doctor
docket project list
docket ticket list --project 3 --json
```

Server precedence: command `--server`, then `DOCKET_URL`, then `http://127.0.0.1:7331`. Actor precedence: `--actor`, then `DOCKET_ACTOR`, then the API's default `Owner`. No configuration files or repository markers are implicitly read. Environment values are not echoed in help.

The server URL must be an HTTP(S) origin without credentials, query, fragment, or subpath, matching the service's configured public origin. `localhost` and `127.0.0.1` are not interchangeable under Host validation. HTTPS certificates are verified normally; no insecure-TLS option is provided.

All client successes are single JSON objects on stdout, using the API's existing object/page shapes. Errors are single JSON objects on stderr, with stdout empty unless writing a result itself failed. Discard stdout whenever the exit status is nonzero. `--json` is accepted for agent conventions, but JSON is already the default and `--json=false` is rejected. Help and bundled skill/reference output are text. Server mode retains operational logging on stderr.

Flags follow the command and its positional ID. Common flags are `--server`, `--actor`, `--timeout` (default `10s`, positive, maximum `1m`), and `--json`. HTTP and JSON-input reads have separate timeout budgets. JSON input must reach EOF within its read budget. File opening/stat calls still depend on the local filesystem. JSON bodies are bounded to 64 KiB and responses to 16 MiB. Non-regular input files, including FIFOs, are rejected; `--input -` is the supported stream input.

## Bundled skill reference

`docket --skill` prints the embedded `docket-cli` SKILL.md verbatim, independent of the working directory or repo/global skill installation. `docket --skill --references` lists available reference filenames when there are multiple; if only one reference is bundled it prints that document directly. Use `docket --skill --references commands.md` or `docket --skill --references safety.md` to print a selected reference. `docket --skill --help` prints discovery usage.

These commands do not parse server configuration, contact HTTP, or open SQLite. Unknown filenames, filesystem paths/traversal, or extra options return exit 2 with a JSON diagnostic on stderr. Output failures return exit 1. Skill reads are bounded to 128 KiB per document and 32 reference entries. The Markdown is compiled into the executable, so changes require rebuilding the CLI rather than runtime file lookup.

## Commands

| Command | Purpose |
| --- | --- |
| `doctor` | Read `/healthz` and require status `ok` |
| `project list` | List projects, including archived projects |
| `project create` | `--name`, `--key`, optional `--statuses` JSON string array |
| `project get ID` | Read a project and version |
| `project update ID` | `--name` and `--statuses` together, plus `--version` |
| `project archive ID`, `project restore ID` | Version-checked reversible archival |
| `ticket list --project ID` | Active tickets, including subtickets |
| `ticket create --project ID` | `--title`, optional `--description`, `--status`, `--assignee`, `--parent` |
| `ticket get ID --project ID` | Read a ticket and version |
| `ticket update ID --project ID` | Supplied `--title`, `--description`, `--status`, `--assignee`, plus `--version` |
| `ticket archive ID --project ID`, `ticket restore ID --project ID` | Version-checked reversible archival |
| `ticket history ID --project ID` | Read immutable change history |
| `comment list TICKET --project ID` | Read discussion |
| `comment add TICKET --project ID` | Append `--body`, with the current ticket's `--version` |
| `events list --project ID` | Read one page of durable project events |
| `serve` | Server/backup mode; see `docket serve --help` |

Use the specific command's `--help` to discover flags. IDs are positive numeric IDs, not project keys or ticket references. Every ticket/comment/event command requires explicit `--project`; no current project is silently inferred.

All list/history commands accept `--after` and `--limit` (1–100). Each invocation retrieves one page, returning `next_cursor`; there is no hidden pagination, polling, acknowledgement, or cursor persistence. Ticket lists also accept `--q`, `--parent`, and `--archived` (archived-only instead of active-only). Keep the same filters when advancing a cursor.

## Mutations and subtickets

Sample IDs and versions below must be replaced with the values returned by your reads/creates:

```sh
docket ticket get 12 --project 3
docket ticket update 12 --project 3 --version 7 --status 'In Progress'
docket comment add 12 --project 3 --version 8 --body 'Ready for review'

docket ticket create --project 3 --title 'Implement the detail' --parent 12
docket ticket list --project 3 --parent 12

docket ticket update 12 --project 3 --version 9 --assignee ''
docket ticket archive 12 --project 3 --version 10
docket ticket restore 12 --project 3 --version 11
```

Omitted PATCH flags preserve their fields; explicit empty values can clear optional text. The CLI never fetches a newer version to silently rebase a write. Subtickets use the existing one-level, same-project rules. Their parent link cannot be edited; `--parent` is available only on creation and listing. Status and archive operations do not cascade.

For multiline or generated data, use a JSON file or stdin instead of field flags:

```sh
docket ticket create --project 3 --input - <<'JSON'
{"title":"Implementation detail","description":"First line\nSecond line"}
JSON

docket ticket update 12 --project 3 --version 7 --input changes.json
```

`--input` accepts one UTF-8 JSON object and cannot be combined with field flags. It contains API write fields, not an entire GET snapshot; version stays in `--version`. The API remains authoritative for field validation.

## Errors and recovery

| Exit | Meaning |
| --- | --- |
| 0 | Success/help |
| 1 | Unexpected server response or output failure |
| 2 | Invalid command/input/configuration or API request rejection |
| 3 | Record not found in the specified project |
| 4 | Version/data conflict |
| 5 | Read unavailable/timed out, oversized/interrupted read, or rate limiting |
| 6 | Mutation outcome uncertain; reconcile before retrying |

Example diagnostic:

```json
{"error":{"code":"conflict","message":"Changed since you loaded it. Reload before trying again.","http_status":409}}
```

Diagnostics may also contain `retry_after` and `outcome_unknown`. The CLI makes no automatic retries and follows no redirects, including on reads. Lost/invalid write responses, write redirects/upstream failures, and broken stdout after a successful write are treated conservatively as uncertain. Fetch the record or list/history before deciding what to do; creation idempotency keys are not implemented. Read a newer version and deliberately reconcile a conflict rather than blindly repeating an edit. Other interruptions, including terminating the CLI yourself, can also leave a committed operation: reconcile before retrying regardless of the exit status.

Docket v1 remains unauthenticated and trusted-network-only. Actor labels are not verified identities. Explicit project IDs prevent accidental route mixups, not unauthorized access. Repository pinning, parent-session ownership, delegation, and assigned-ticket-only guards remain the separate Pi integration's responsibility; the raw CLI does not implement them.

## Isolated verification

Go unit/integration checks run with race detection and vet through `make test`. The process-level CLI checks require Node 24+ but no npm dependencies. Browser checks additionally require agent-browser/Chromium.

```sh
build_directory=$(mktemp -d /tmp/docket-cli-check-XXXXXX)
make test
make test-cli BUILD_DIRECTORY="$build_directory"
DOCKET_SCREENSHOTS=/tmp/docket-cli-screenshots make test-browser BUILD_DIRECTORY="$build_directory"
```

Tests launch disposable databases and ephemeral loopback listeners, explicitly target those servers, and never stop/restart the installed service. `DOCKET_BINARY` can select an already-built executable when invoking the Node test files directly. Testing a separate build directory avoids replacing a repository binary used by a live service.
