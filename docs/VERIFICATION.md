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

## Archive, copy-link, live updates, and free-text assignees

Projects and tickets now have independent reversible archive flags. Tickets also have an optional trimmed assignee label (100 UTF-8 bytes, empty means unassigned). Assignment is descriptive, not an account or permission. Existing version-checked mutations and transactional snapshots record these changes; there are no DELETE routes.

Schema v2 is created for new databases and migrated transactionally from v1. Migration checks seed the released v1 tables with projects, tickets, comments, and a nonzero event cursor, verify active/unassigned defaults, then verify assignment/archive persistence through reopening and backup restoration. A forced failure during the second ALTER verifies rollback of the first ALTER and preservation of the old schema version and records. An older executable cannot open the upgraded schema; backup-based rollback is documented in README.

The UI copies the stable project URL, provides a selectable-link fallback if clipboard access fails, and separates active/archived projects and tickets. Real clipboard contents were verified in an isolated Chromium session with a temporary origin-scoped permission. The CDP connection must remain open for that permission override to remain effective. The fallback check intentionally rejects only the clipboard write in the disposable browser; it is not evidence of compatibility with every clipboard implementation.

Visible tabs poll every three seconds without overlapping cycles. A bounded tail query captures the project cursor before reading a board snapshot, avoiding full history replay and closing the bootstrap/snapshot race. Subsequent events refresh the visible board; open tickets and discussion synchronize without erasing drafts or advancing a dirty form's version. Hidden tabs skip polling. Search, archive filters, and already loaded ticket pages survive updates.

Failing-before / passing-after checks retained:

- Project and ticket archive PATCH requests initially returned 400 for unknown fields; the same behavior checks now pass archive, restore, stale-write rejection, discussion/history retention, and rollback on event insertion failure.
- Copy URL and ticket archive browser checks initially failed because the actions were absent; they now pass real clipboard copying, clipboard fallback, archive cancellation, restoration, and retained records/drafts.
- The live-update check initially timed out waiting for an external API change to appear. It now passes external ticket edits, comments, project creation/archiving, ticket archive/restore, preservation of 101 loaded tickets and search, dirty draft preservation, and conflict rejection without a document reload.
- Assignment PATCH initially returned 400 and the browser input check failed because the field was absent. Both now pass create/edit/clear, UTF-8 bounds, trimmed text, stale-write protection, durable snapshots, rollback, literal HTML-shaped labels, and live draft preservation.

Current full validation: 20 Go API/store tests with race detection and vet; all eight real-browser/process tests; Svelte/TypeScript checking with zero errors/warnings; production build; baseline-CPU QEMU backup and all 20 API/store tests; module/package/final-binary vulnerability scans; npm audit with zero known vulnerabilities; and whitespace checks. Desktop and phone screenshots were inspected. Checks used disposable databases and browser sessions. No all-browser certification, heavy multi-tab/load test, instantaneous push guarantee, or failing-disk guarantee is claimed. All polling remains subject to existing UI display and global request limits.

Direct correctness review found no blocking issues in the scoped changes. Complexity review: lean already; no new application dependency or persistent connection machinery was introduced.

The local running instance was backed up with its previous executable before restart, then upgraded to schema v2. Read-only comparisons against the pre-upgrade backup verified every pre-existing project, ticket, comment, and event value, plus active/unassigned defaults and SQLite integrity. A read-only browser smoke check confirmed the deployed copy/archive controls and ticket assignee field. Backup and rollback executables remain in ignored local directories and are not repository contents.

## Project settings and gear entry

Project settings now opens from an icon-only gear button with an accessible label and tooltip. The responsive settings dialog separates general details, workflow, and project lifecycle controls, with a scrollable body and persistent action footer. The board header no longer exposes project removal. Inside settings, **Delete project** explicitly explains reversible archival and retained tickets/comments/history; archived projects offer **Restore project** there. Ticket actions and the API/schema are unchanged by this UI revision.

The focused browser check failed against the prior executable because settings still used a text button. After the change it passes the gear label/tooltip, action location, archive helper, cancellation and retained drafts, discarding unsaved invalid settings only after confirmation, stale restore rejection despite live refresh, successful restore, absence of deletion on new-project forms, and accessible phone-width actions. Existing archive/clipboard checks were updated for the requested settings entry point without changing their data-retention expectations.

Validation: all 20 backend tests with race detection and vet, all nine real-browser/process tests, type checking with zero errors/warnings, production build, and whitespace checks pass. Desktop and phone settings screenshots were inspected. Direct review found no blocking correctness issues; complexity is lean already. No dependencies or API routes were added for this revision.

## Status editor visual refresh

The status editor now uses numbered, separated rows, grouped up/down buttons, a first-position Default badge, a heading capacity counter, and a full-width add action. Collapse/Edit statuses replaces the ambiguous Done label. The default helper explains that new tickets start in the first status; the in-use-status warning remains. New-project mode stays compact, and settings mode uses roomier rows with bounded scrolling. No workflow logic, API, schema, or dependencies changed.

The retained workflow check failed against the previous executable because the Default indicator was absent. It now verifies that the indicator follows the first position after reordering, the helper is associated with the first input, count/collapse labels are clear, keyboard Enter can reorder, and newly created tickets actually use the first saved status. Existing validation, in-use protection, 1–12 bounds, add/remove, draft reset, and persistence expectations remain intact. An initial enlarged layout clipped a lower settings control; reduced redundant spacing and a thinner list scrollbar restored access without weakening those checks.

All 20 backend tests with race detection and vet, all nine browser/process tests, type checking, production build, and whitespace checks pass. Desktop and phone settings/create screenshots were inspected. Direct review found no blocking correctness issues; complexity is lean already. Other browser engines and full assistive-technology compatibility remain unverified.

## Active-only default ticket queries

Ticket collection requests now select active tickets by default. Missing or empty `archived` means false; only `archived=true` selects archived tickets. The SQL WHERE clause applies the archive state before ORDER BY/LIMIT, alongside project scope, title search, and cursor conditions. The frontend was not changed and still requests archive states explicitly. Direct ticket, comment, history, and event access is unchanged. This intentionally supersedes the earlier include-both default described at the archive feature checkpoint; clients needing both sets must query each explicitly.

The new regression check failed against the old query: an unfiltered fixture with four tickets returned all four instead of its two active tickets. After replacing the nullable filter with a false boolean default and a required SQL archive predicate, all 21 backend tests with race detection and vet pass. The check covers omitted/empty/false/true filters, search, pagination across interleaved archive states, empty-page cursor retention, invalid filters, project isolation, and direct access to an archived ticket. A real-server browser/process check also asserts that an unfiltered GET hides an archived ticket while an explicit archive GET returns it.

The full screenshot-enabled browser suite passes all nine tests. A separate run without screenshots passed eight tests but hit a pre-existing workflow-editor footer/viewport targeting failure; the same failure was reproduced against the executable from before this API change. No UI production code or workflow expectations were changed to hide that unrelated limitation. Direct review found no blocking API correctness issues; complexity is lean already. API documentation and README describe the new default and compatibility consequence.

## Desktop board drag-and-drop

Active ticket cards now support native desktop dragging between configured status columns, including drops over existing destination cards. Valid targets are highlighted and source cards show drag/pending feedback. Same-column and cancelled drops do not write. Archived tickets remain non-draggable; the existing ticket editor and native Status selector remain available for keyboard/touch use. Dragging changes status only, not within-column ordering or project membership.

Each gesture holds one ticket snapshot and board generation. The drop uses the original ticket version in the existing project-scoped PATCH route's If-Match header, sending only status. There is at most one pending drop, and the UI accepts the saved API response rather than moving a card optimistically without persistence. The bounded board snapshot is reconciled afterward; failures are visible, uncertain outcomes are not blindly retried, and late responses cannot populate a different project after navigation. Board reads block drag initiation, while metadata-only polling does not. Polling checks the drag/pending state both before and after its awaited reads so an already-running poll cannot rebase or replace the source mid-gesture. Transferable drag data is cleared and contains only the ticket identifier, not ticket text.

The initial behavior check failed against the old executable because ticket cards were not draggable. After implementation, real Chromium native drag/drop events (verified as trusted events) persist the new status/version and history, update column counts, retain title/description/assignee/discussion, leave the destination ticket unchanged, clean up cancelled drags, preserve keyboard editor access, and disable archived-ticket dragging.

A second real-application check holds a native gesture across a polling interval and a previously paused metadata read, updates the ticket through the API, and proves that a stale drop returns conflict without overwriting the authoritative record. It exercises actual Chromium offline mode and recovery, then delays and forwards a real move request while navigating to another project, verifying a single commit to the original ticket and no changes or card leakage in the new project. The held gesture uses Chromium's native intercepted drag data through CDP, not a fabricated DOM DataTransfer; early driver attempts missing held-button protocol fields were harness failures, not application regression evidence. Delayed fetches forward actual application responses and do not mock Svelte or server compatibility.

Validation: all 21 backend tests with race detection and vet, zero-error/warning Svelte/TypeScript checking, production build, all 11 screenshot-enabled browser/process tests, npm audit with zero known vulnerabilities, and whitespace checks pass. A live-drag screenshot was inspected. Direct review found no blocking correctness issues; complexity is lean already. No dependencies, API routes, event kinds, or schema changes were introduced. The previously recorded default-viewport workflow footer targeting limitation is unrelated and unchanged. Touch dragging, other browser engines, and response-loss-after-commit fault injection were not verified; the editor fallback and explicit reconciliation/error handling remain necessary.
