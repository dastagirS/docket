# Implementation verification

Date: 2026-10-26. Scope: standalone Docket v1, not the future Pi integration. The original artifact details below describe that implementation checkpoint; later workflow-editor and Svelte UI revisions are recorded at the end.

## Final passing checks

- `make test`: 14 API/store tests with Go's race detector, plus vet, in the pinned Go 1.27.3 builder.
- `make verify-portable`: actual SQLite initialization/backup and the same 14 tests under QEMU's baseline `qemu64` CPU model. These emulated tests do not use the race detector; native builder tests do.
- `node --test tests/browser.test.mjs`: two real-application tests pass. One drives a uniquely named Chromium session through project/ticket creation, comments, status changes, board/list views, stored-text safety, delayed-navigation races, stale project forms, phone-width creation, and visible refresh failures. The other kills/restarts real processes and verifies event replay and backup restoration.
- `make security`: module, package, and final binary scans with pinned govulncheck v1.9.0 pass. Module checksums verified. Native builder package-feed comparison: 31 packages, 184 version comparisons, zero older-than-fixed findings. See SECURITY.md for coverage limits.
- Non-root scratch Docker image: creates and reads projects/tickets/events with a read-only root filesystem, dropped capabilities, no-new-privileges, bounded memory/PIDs, and a local named volume. Events remain byte-for-byte identical after removing and recreating the container.
- Exported executable and `/docket` extracted from the runtime image have identical SHA-256: `6bac9b4c9324e73c950067cfad317987acbee945b42222e858ff821906e7014f`.
- Final ELF is static, has no dynamic interpreter, and declares no above-baseline x86 ISA requirement. Linux amd64 only; other operating systems, architectures, and every physical CPU/kernel were not tested.

The API suite covers transactional mutation/event rollback, immutable history/replay, stale writes, eight simultaneous writers, competing independent SQLite connections, wrong-project ticket/comment/history references, status configuration conflicts, clean reopening, consistent backups, event-capacity failure without eviction, pagination/project feed isolation, browser Origin/Host protection, rate limits/cancellation, malformed/oversized input, unsupported schema versions, literal SQL/HTML-shaped text, and invalid UTF-8.

## Actual failing-before / passing-after evidence

| Check | Before | After |
| --- | --- | --- |
| Saving a ticket with an unsent comment | Editor closed and draft disappeared | Editor/draft retained; real-browser suite passes |
| Delayed older project navigation | Older request changed URL after a newer board selection | Latest selection owns both URL and board |
| Project form during background refresh | Refresh advanced form version and accepted stale values | Original form version retained; stale write rejected |
| Post-commit list refresh failure | Error hidden in a closed dialog | Visible notification; committed project confirmed through API |
| Invalid raw UTF-8 JSON | Returned 201 after silently replacing invalid text | Returns 400 before JSON replacement; API test passes |
| Phone-width project creation | Action had no layout rectangle / was hidden | Action visible and real form submission exercised |
| Host-specific binary | ELF check failed on x86-64-v4 requirements | Generic ELF check and baseline-emulated DB/API tests pass |
| Go toolchain advisory | Binary scan flagged GO-2026-6809 in Go 1.27.2 | Final Go 1.27.3 binary scan passes |

Negative execution of the original binary under baseline QEMU emitted `Illegal instruction` but the emulator did not exit cleanly; those attempts were timed out/removed and are not clean exit-code regression proof. The bounded ELF assertion is the authoritative failing-before portability check. Positive execution of the final binary and tests completed normally.

One initial project-form race harness delayed a second refresh too; it was explicitly released before recording the stale-overwrite result. One initial browser run reached a hidden phone action; the focused visibility assertion then reproduced the actual bug before the CSS fix. No unrelated harness failure is counted as proof of an application fix.

## Direct review

Reviewed the complete new Go implementation, browser assets, tests, deployment files, and documentation directly, without a reviewer subagent. Correctness findings above were fixed with retained behavior checks. Final review: no remaining blocking correctness findings. This does not imply exhaustive security or lifecycle coverage. Complexity review: lean already; no framework, ORM, event broker, frontend build system, or additional application module dependency was added.

## Evidence and cleanup

Local verification artifacts contain synthetic test data, logs, screenshots, inventory, and detailed diffs. Machine-local evidence directories are not published in this repository and are not runtime dependencies. Temporary application/browser sessions, test containers, and test volumes were removed. Six pre-existing running Docker containers retained their IDs. Build images/layers and normal Go/Docker caches remain; one host metadata command also populated Go's toolchain cache without changing the system Go package.

## Limits and deferred work

No production-scale load test, sustained outage campaign, power-cut/filesystem-failure proof, independent native source audit, or all-browser/accessibility certification. A process-kill/restart test is not a power-failure test. Browser delay/failure injection proves the exercised races/error paths, not all network behavior. The main database cap excludes WAL, temporary files, backups, and unrelated disk users.

No accounts/API keys, project authorization, Pi repository initialization, repo/subagent enforcement, atomic parent ownership, agent spawning, deletion, or outgoing webhooks were implemented. Those boundaries are explicit in README, CONTEXT.md, API.md, and TODO.md. Project-scoped routes reject accidental wrong-board references but are not security isolation.


## Workflow-editor UI revision

Replaced the newline textarea with compact default-status chips and a collapsible inline editor: add/remove, up/down ordering, subtle status dots, and accessible row labels. New-project forms start collapsed; existing-project settings open expanded. The same status-array API, version preconditions, byte/count limits, and in-use-status protection remain unchanged. No dependency, Go source, or database schema changed.

The new real-browser behavior check failed against the old executable because compact status chips were absent. After implementation it passes creation, inline editing, both reorder directions, remove/add, 1–12 bounds, blank/duplicate/UTF-8 byte validation, reopening and focusing invalid collapsed rows, saved-order persistence, server rejection of changes to in-use statuses, and fresh-project draft reset. Desktop and 390px phone screenshots were inspected.

A repeated run at 720px viewport height exposed action targeting failures as the growing status list shifted the whole dialog. The editor list is now height-bounded and scrolls focused rows locally. The check waits for each observable row-count change and verifies that workflow actions remain visible at the 12-status limit; expected behavior was not weakened. Repeated full-suite runs passed after that fix.

All 14 Go API/store tests with race detection and vet, all three real-browser/process tests, baseline-CPU verification, and module/package/binary vulnerability checks passed after the UI change. Evidence, diffs, and screenshots were retained locally, not published. Checks used disposable application databases, not the user's running board. Other browsers, native phone keyboards, and full assistive-technology compatibility are not certified by these checks.

## Svelte frontend migration

The current frontend is Svelte 5 with TypeScript and Vite, replacing the original framework-free UI described above. The Go API and SQLite schema did not change. Docker builds the frontend from the npm lockfile and embeds the output in the executable; generated assets, Node dependencies, live data, and binaries are not source files.

Checks executed after migration:

- `make test`: all 14 API/store tests with race detection and vet pass.
- `make build`: production frontend type checking, Vite bundling, and static Go compilation pass.
- `node --test tests/browser.test.mjs`: all four real-browser/process tests pass, including stale ticket rejection, reload, and commenting with the current version.
- The browser suite verifies the module entry, rendered SVG sprite, loaded local Manrope font, unchanged strict CSP, automatic project keys, responsive controls, and existing application workflows.
- `npm --prefix frontend audit`: zero known vulnerabilities in the locked dependency tree at this checkpoint.
- The loopback Vite development server was checked with an existing-project deep link, proxied API reads, font loading, and rendered icons. No live records were changed for that check.

The icon check failed against the old executable because decorative SVGs were absent, then passed with the new bundle. Enlarged font/icon metrics exposed clipped dialog controls; existing workflow checks failed before layout adjustments and passed afterward without weakening their expected behavior. Desktop and phone screenshots were inspected. Baseline-CPU and Go vulnerability scans listed in earlier sections were not rerun for this frontend migration; those results describe their earlier checkpoints, not fresh scans of the new executable.
