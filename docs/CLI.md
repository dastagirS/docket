# Docket CLI

The same native executable provides a short-lived API client and an explicit `serve` command. Client commands never open SQLite or automatically start/stop a service. The browser UI and CLI share the existing HTTP API and history/version rules.

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

A bare `docket` prints help. Start the server explicitly with `docket serve`; use `docket serve --help` for its options.

## Connection and output

```sh
export DOCKET_URL=http://127.0.0.1:7331
export DOCKET_ACTOR='Build agent'  # optional attribution label, not authentication

docket doctor
docket project list
docket ticket list --project 3 --json
```

Without a binding, server precedence is command `--server`, then `DOCKET_URL`, then `http://127.0.0.1:7331`. Actor precedence: `--actor`, then `DOCKET_ACTOR`, then the API's default `Owner`. With no explicit `--project`, ticket/comment/event commands look up the current folder in `~/.docket/registry.json` and use its project **and** pinned server, ignoring `DOCKET_URL`. A different `--server` requires explicit `--project`; explicit `--project` bypasses folder defaults entirely (supply `--server` too when selecting a different instance). Project commands and `doctor` use the bound server unless `--server` is explicit. Environment values are not echoed in help.

The server URL must be an HTTP(S) origin without credentials, query, fragment, or subpath, matching the service's configured public origin. `localhost` and `127.0.0.1` are not interchangeable under Host validation. HTTPS certificates are verified normally; no insecure-TLS option is provided.

All client successes are single JSON objects on stdout, using the API's existing object/page shapes. Errors are single JSON objects on stderr, with stdout empty unless writing a result itself failed. Discard stdout whenever the exit status is nonzero. `--json` is accepted for agent conventions, but JSON is already the default and `--json=false` is rejected. Help and bundled skill/reference output are text. Interactive init prompts also use stderr; noninteractive invocations remain JSON-only. Server mode retains operational logging on stderr.

Flags follow the command and its positional ID. Common flags are `--server`, `--actor`, `--timeout` (default `10s`, positive, maximum `1m`), and `--json`. HTTP and JSON-input reads have separate timeout budgets. JSON input must reach EOF within its read budget. File opening/stat calls still depend on the local filesystem. JSON bodies are bounded to 64 KiB and responses to 16 MiB. Non-regular input files, including FIFOs, are rejected; `--input -` is the supported stream input.

## Initialize a project folder

```sh
cd /path/to/your/project
docket project init
docket ticket create --title 'First task'
```

The CLI keeps **one user-level `~/.docket` directory**, not a marker in every project:

```text
~/.docket/
  registry.json        # folder → server/project bindings (mode 600)
  init.lock            # present only while init is running/incomplete
```

The directory is mode 700. Each version-1 registry entry stores `version`, canonical absolute `folder`, `project_url`, immutable `project_key`, and `project_created_at`; the registry has its own `version` and `bindings` array. It contains no credentials or database. Lookup walks at most 64 ancestors and stops at the nearest Git boundary. The nearest binding wins; nested repositories do not inherit an outer board. No global last-used project/server is inferred. Symlink folder aliases resolve to the canonical folder; moved/copied folders need deliberate registration. Invalid/deleted entries do not authorize another board; deleted paths may remain recorded without preventing other folders from working. Bindings are path-based: recreating a folder at a registered path reuses its board, so run init deliberately when repurposing a path.

First init creates the API project and atomically records the current folder. An existing ancestor binding is reused rather than silently shadowed. Re-init shows the project and total/active/archived ticket counts, including subtickets, then offers:

1. **Keep existing** (Enter/default): no remote mutation; an existing registry entry is not rewritten.
2. **Create another**: preserve the old project and replace only this folder's association.
3. **Archive and replace**: create a new project, archive the old one using its observed version, then replace this association. Tickets/history remain intact; keys stay reserved even after archival.

Noninteractive repeats without an explicit choice exit 2 without mutation:

```sh
docket project init --name 'Service' --key SERVICE --server http://127.0.0.1:7331
docket project init --on-existing keep
docket project init --on-existing create --name 'Next service' --key NEXT
docket project init --on-existing archive --name 'Replacement' --key REPLACE
```

Name/key default from the folder/name; keys need 2–10 ASCII letters/digits and must be unique. Prompts are bounded to 4 KiB/two minutes; `--interactive` permits controlled piped input. EOF/invalid input cancels. JSON output includes the registry filename as `marker` and the canonical `folder`; keep also returns `tickets`. Help is offline and creates no state. Ordinary lookup does not create the registry directory either.

Implicit ticket requests verify the stored immutable project identity. Malformed/nonprivate/oversized metadata and symlinks are rejected. The registry is capped at 1 MiB/1,024 entries; scope is not broadened on errors. Implicit writes refuse archived projects as a convenience guard, while explicit authorized API/CLI edits remain available. Bindings are not authorization and never change Pi integration pins, ownership, assigned-ticket restrictions, or receipts.

### Interrupted init

`~/.docket/init.lock` serializes registry-changing init operations without retrying or stealing a lock. It records the canonical folder and operation phases. Existing unrelated registered folders remain usable; init for another folder must wait for explicit retry after the current operation finishes. Corrupt journals with unknown scope fail closed. One init cannot overwrite bindings committed by another, and detected external registry edits are retained rather than overwritten.

Creation, archival, and registry rename are **not one transaction**. The replacement is created before old-project archival so a duplicate-key failure leaves the old project intact. A stale/unknown write or filesystem failure may retain a created replacement and journal (exit 6). The affected folder's implicit commands stop until reconciliation; no replay, rollback, or automatic lock expiry occurs. Existing processes snapshot their original binding rather than being retargeted by a later init.

Confirm no init is running, inspect the home journal and registry, then inspect the approved server's projects/history with explicit `--server` (and `--project` for ticket/event commands). Resolve the intended association and old/new state with human approval before removing the journal/staged `.registry-write-*` files. An interruption after rename may already have updated the registry. Partial journal writes/disk failures may require server-history inspection; no power-loss/hostile-same-user guarantee is claimed.

## Bundled skill reference

`docket --skill` prints the embedded `docket-cli` SKILL.md verbatim, independent of the working directory or repo/global skill installation. `docket --skill --references` lists available reference filenames when there are multiple; if only one reference is bundled it prints that document directly. Use `docket --skill --references commands.md` or `docket --skill --references safety.md` to print a selected reference. `docket --skill --help` prints discovery usage.

These commands do not parse server configuration, contact HTTP, or open SQLite. Unknown filenames, filesystem paths/traversal, or extra options return exit 2 with a JSON diagnostic on stderr. Output failures return exit 1. Skill reads are bounded to 128 KiB per document and 32 reference entries. The Markdown is compiled into the executable, so changes require rebuilding the CLI rather than runtime file lookup.

## Commands

| Command | Purpose |
| --- | --- |
| `doctor` | Read `/healthz` and require status `ok` |
| `project init` | Create a project and home-registry folder binding; repeat with keep/create/archive choice |
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

Use the specific command's `--help` to discover flags. IDs are positive numeric IDs, not project keys or ticket references. Ticket/comment/event commands require either explicit `--project` or a folder binding in the home registry. Existing-record mutations still require explicit `--version`.

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
| 6 | Mutation outcome uncertain or initialization incomplete; reconcile before retrying |

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
