# Docket API v1

Base URL: your configured origin, e.g. `http://127.0.0.1:7331`. No accounts, tokens, or API keys. All exposed API operations are available to anyone who can reach it.

JSON writes require `Content-Type: application/json`; unknown fields, trailing JSON, invalid/oversized UTF-8 text, and cross-project ticket references are rejected. Descriptions/comments are plain text. `X-Docket-Actor` is an optional display label, default `Owner`, **not authenticated attribution**. Clients may spoof it; do not use it for authorization.

## Routes

| Method | Path | Behavior |
| --- | --- | --- |
| GET | `/healthz` | Database connectivity |
| GET, POST | `/api/projects` | List/create projects |
| GET, PATCH | `/api/projects/{project}` | Read/update project |
| GET | `/api/projects/{project}/stats` | Ticket counts: `{total, active, archived}` |
| GET, POST | `/api/projects/{project}/tickets` | List/create tickets |
| GET, PATCH | `/api/projects/{project}/tickets/{ticket}` | Read/update ticket |
| GET, POST | `/api/projects/{project}/tickets/{ticket}/comments` | List/append comments |
| GET | `/api/projects/{project}/tickets/{ticket}/history` | Immutable ticket change history |
| GET | `/api/projects/{project}/events` | Durable project change feed |

IDs are positive integers. A ticket ID paired with the wrong project returns `404`, including for comments and history. There is no global ticket endpoint. This prevents accidental scope mixups; clients can still explicitly select any reachable project.

## Requests

```sh
curl -H 'Content-Type: application/json' \
  -d '{"name":"Website","key":"WEB"}' \
  http://127.0.0.1:7331/api/projects

curl -H 'Content-Type: application/json' \
  -d '{"title":"Improve the home page","description":"Keep it simple"}' \
  http://127.0.0.1:7331/api/projects/1/tickets

curl -X PATCH -H 'Content-Type: application/json' -H 'If-Match: "1"' \
  -H 'X-Docket-Actor: integration-name' -d '{"status":"In Progress"}' \
  http://127.0.0.1:7331/api/projects/1/tickets/1

curl -H 'Content-Type: application/json' -H 'If-Match: "2"' \
  -d '{"body":"Implementation ready for review"}' \
  http://127.0.0.1:7331/api/projects/1/tickets/1/comments
```

Project creation: `name`, unique case-normalized `key` (2–10 ASCII letters/digits), optional `statuses` (ordered string array). Default statuses are documented in README. Projects expose `archived` (initially `false`). Project PATCH accepts `name` and `statuses` together, an `archived` boolean, or both. Key cannot change. Removing a status in use returns `409`; move its tickets first. `{"archived":true}` archives and `{"archived":false}` restores, with the usual `If-Match` precondition.

Ticket creation requires `title`; `description` and `assignee` default empty and `status` defaults to the project's first status. `assignee` is a trimmed, free-text label of at most 100 UTF-8 bytes, not an account, authorization rule, or authenticated identity. An empty string means unassigned. Tickets expose `archived` (initially `false`); archive existing tickets with PATCH, not creation. Ticket PATCH accepts any nonempty subset of `title`, `description`, `status`, `assignee`, and `archived`. Set `assignee` to `""` to unassign. Comment POST accepts `body` and returns `{comment, ticket_version}`.

Tickets also expose `parent_id`: `null` for a top-level ticket, or the ID of its parent. On creation, optional `parent_id` must be `null` or a positive integer naming an existing top-level ticket in the same project. A missing/wrong-project parent returns `404`; invalid IDs or using a subticket as a parent return `400`. The parent link is immutable: any ticket PATCH containing `parent_id`, including `null`, returns `400`. Existing tickets cannot be converted or reparented in this version. This enforces exactly one level without recursive traversal or cycles.

Subtickets are normal tickets in the main collection, not hidden nested records. They have independent status, assignment, discussion, history, version, and archive state and count toward the normal ticket limit. Creating or changing a subticket does not update its parent's fields/version/history, and archiving either side does not archive the other. Archived parent records remain usable like other archived records. To create a subticket, POST e.g. `{"title":"Implement the detail","parent_id":1}` to its project's ticket collection.

Archiving projects or tickets never deletes records or changes their IDs/keys. Archived records remain readable and editable through the same routes; comments and history remain available. Project archiving does not change individual ticket archive flags. All archived records still count toward capacity limits. The project list includes both active and archived projects. Ticket lists return **active tickets only by default**, including when `archived` is omitted or empty. `?archived=false` explicitly selects active tickets; `?archived=true` explicitly selects archived tickets only. Archive filtering is applied by the API's SQL query before pagination, not by the client. Invalid archive filter values return `400`. Clients that previously relied on an unfiltered list including both states must now query the active and archived lists separately.

Project/ticket GET returns an ETag such as `"3"`. Every project PATCH, ticket PATCH, and comment POST requires the quoted current version in `If-Match`. Missing precondition: `428`; malformed precondition: `400`; stale version: `409`. A comment advances its ticket's version. Project and ticket versions are independent.

Writes are transactions: a successful mutation and its event/history are committed together. Request acceptance does not give exactly-once delivery of an HTTP response. Do not blindly retry a creation after a timeout: reload to check the outcome. Creation idempotency keys are not implemented. A unique project key prevents duplicate keys, not duplicate ticket creation.

Project stats count **all tickets, including subtickets**, grouped by their own archive flags. They include archived tickets and remain available for archived projects; project archival does not alter these counts. Missing projects return `404`. Counts are informational snapshots, not a version precondition. The aggregate query avoids downloading ticket pages just to display a count.

## Pagination, search, and replay

All collection routes return `{ "items": [...], "next_cursor": 42 }` in ascending ID order. Use `?after=42&limit=100`. Default/max page size 100. A short page means the feed/list is currently caught up; an empty page preserves the supplied cursor. Gaps in IDs are normal, especially in a project-scoped event feed.

Ticket lists accept `q` for substring title search (SQLite's built-in ASCII case folding). Search scope is the route's project, not every project. An optional positive `parent_id` selects only direct children of that same-project ticket; missing/wrong-project parent IDs return `404`, and malformed, empty, or repeated parent filters return `400`. Parent, archive, and search predicates apply in SQL before pagination. Without `parent_id`, both top-level tickets and subtickets are included. Continue using the same search string, `archived`, and `parent_id` filters while paging.

Project event feeds additionally accept `?tail=true&limit=1` to bootstrap at the newest event without replaying the entire feed. Tail pages contain the newest bounded set of events in ascending ID order; `next_cursor` is the newest returned event ID (zero if there are none). Tail mode cannot be combined with a positive `after` cursor or used on ticket history. Capture a tail cursor **before** reading the initial board snapshot, then poll normally with `after` to avoid losing mutations that race the snapshot. Tail mode is for snapshot-based UI initialization, not a substitute for an integration's durable replay cursor.

Events contain `id`, `project_id`, optional `ticket_id`, `kind`, `actor`, `created_at`, and `data`:

- `project.created`, `project.updated`: complete project snapshot. Archive/restore uses `project.updated` with the new `archived` state.
- `ticket.created`, `ticket.updated`: complete ticket snapshot, including `assignee`, `archived`, and nullable `parent_id`. Assignment and archive/restore changes use `ticket.updated`.
- `comment.created`: `{comment, ticket_version}`.

History uses these same committed records; no independent best-effort notification step exists. Integrations poll the project feed, process a page, and **persist the cursor only after successful processing**. A consumer crash can replay already-processed events: deduplicate by event ID or use idempotent handling. Each consumer stores its own cursor; Docket has no consumer registration/acknowledgement endpoint. There is no outbound webhook or automatic task launch.

The browser polls about every three seconds while visible, with no overlapping poll cycles. It refreshes project navigation and changed board snapshots, preserves the current search/archive filter and number of loaded pages, and updates open tickets/discussion without a document reload. Unsaved ticket fields retain their original version and values when an external change arrives; the UI offers reload and a stale save still returns `409`. Unsent comment drafts are preserved. Polling is bounded by the documented page/display limits and counts toward the global rate limit. This is eventual polling, not instantaneous push or an offline guarantee.

Legacy events written before schema v2 may omit `archived` and `assignee`; do not rewrite old history. Consumers should interpret absent archive/assignee fields in those older snapshots as active/unassigned.

Legacy events written before schema v3 may omit `parent_id`; interpret those tickets as top-level. Migration does not rewrite history.

Events survive ordinary restarts, process kills, and consistent backups. No event expiry/eviction. Data-directory replacement or restoration to an older snapshot is not an ordinary restart: consumers must explicitly reconcile lost/newer history and their saved cursors. No exactly-once or network-partition guarantee is claimed.

## Bounds and errors

| Resource | Bound |
| --- | --- |
| Projects / tickets per instance | 128 / 10,000 |
| Events per instance | 250,000; writes fail instead of pruning |
| SQLite main database | 512 MiB at 4096-byte pages; WAL/temporary/backup files need additional disk headroom |
| Statuses per project | 1–12 unique, trimmed names |
| Name / status / title / assignee | 100 / 100 / 200 / 100 UTF-8 bytes |
| Description / comment / actor label | 16,000 / 16,000 / 80 UTF-8 bytes |
| Request body / headers | 64 KiB / 16 KiB |
| Page size | 1–100 |
| Concurrent handlers / SQLite connections | 32 / 1 per process |
| Requests | 600/minute globally, including static files/health; `429` with `Retry-After` |
| Database request context / lock wait | 5 seconds / 1 second |
| HTTP header/read/write/idle timeout | 5 / 10 / 15 / 30 seconds |
| UI displayed tickets / activity | 1,000 / 500 per comments/history list; full API record remains available |

`400`: invalid input; `403`: Host/Origin mismatch; `404`: missing/wrong-project resource; `409`: version/data conflict; `415`: wrong content type; `428`: version required; `429`: rate limit; `503`: busy, interrupted, full, or capacity exceeded; `500`: unexpected failure. API errors use `{ "error": "safe explanation" }`. HTTP router errors may be plain text. No request data or database error detail is logged. An interrupted response can leave a committed operation: reload before retrying.

The main database cap does not reserve disk space, cap WAL/backup files, or protect unrelated disk users. SQLite I/O errors are handled, not evidence that a failing disk can never lose data. Keep independent backups and free storage.
