# Docket API v1

Base URL: your configured origin, e.g. `http://127.0.0.1:7331`. No accounts, tokens, or API keys. All exposed API operations are available to anyone who can reach it.

JSON writes require `Content-Type: application/json`; unknown fields, trailing JSON, invalid/oversized UTF-8 text, and cross-project ticket references are rejected. Descriptions/comments are plain text. `X-Docket-Actor` is an optional display label, default `Owner`, **not authenticated attribution**. Clients may spoof it; do not use it for authorization.

## Routes

| Method | Path | Behavior |
| --- | --- | --- |
| GET | `/healthz` | Database connectivity |
| GET, POST | `/api/projects` | List/create projects |
| GET, PATCH | `/api/projects/{project}` | Read/update project |
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

Project creation: `name`, unique case-normalized `key` (2–10 ASCII letters/digits), optional `statuses` (ordered string array). Default statuses are documented in README. Project PATCH requires both `name` and `statuses`; key cannot change. Removing a status in use returns `409`; move its tickets first.

Ticket creation requires `title`; `description` defaults empty and `status` defaults to the project's first status. Ticket PATCH accepts any nonempty subset of `title`, `description`, `status`. Comment POST accepts `body` and returns `{comment, ticket_version}`.

Project/ticket GET returns an ETag such as `"3"`. Every project PATCH, ticket PATCH, and comment POST requires the quoted current version in `If-Match`. Missing precondition: `428`; malformed precondition: `400`; stale version: `409`. A comment advances its ticket's version. Project and ticket versions are independent.

Writes are transactions: a successful mutation and its event/history are committed together. Request acceptance does not give exactly-once delivery of an HTTP response. Do not blindly retry a creation after a timeout: reload to check the outcome. Creation idempotency keys are not implemented. A unique project key prevents duplicate keys, not duplicate ticket creation.

## Pagination, search, and replay

All collection routes return `{ "items": [...], "next_cursor": 42 }` in ascending ID order. Use `?after=42&limit=100`. Default/max page size 100. A short page means the feed/list is currently caught up; an empty page preserves the supplied cursor. Gaps in IDs are normal, especially in a project-scoped event feed.

Ticket lists accept `q` for substring title search (SQLite's built-in ASCII case folding). Search scope is the route's project, not every project. Continue using the same search string while paging.

Events contain `id`, `project_id`, optional `ticket_id`, `kind`, `actor`, `created_at`, and `data`:

- `project.created`, `project.updated`: complete project snapshot.
- `ticket.created`, `ticket.updated`: complete ticket snapshot.
- `comment.created`: `{comment, ticket_version}`.

History uses these same committed records; no independent best-effort notification step exists. Integrations poll the project feed, process a page, and **persist the cursor only after successful processing**. A consumer crash can replay already-processed events: deduplicate by event ID or use idempotent handling. Each consumer stores its own cursor; Docket has no consumer registration/acknowledgement endpoint. There is no outbound webhook or automatic task launch.

Events survive ordinary restarts, process kills, and consistent backups. No event expiry/eviction. Data-directory replacement or restoration to an older snapshot is not an ordinary restart: consumers must explicitly reconcile lost/newer history and their saved cursors. No exactly-once or network-partition guarantee is claimed.

## Bounds and errors

| Resource | Bound |
| --- | --- |
| Projects / tickets per instance | 128 / 10,000 |
| Events per instance | 250,000; writes fail instead of pruning |
| SQLite main database | 512 MiB at 4096-byte pages; WAL/temporary/backup files need additional disk headroom |
| Statuses per project | 1–12 unique, trimmed names |
| Name / status / title | 100 / 100 / 200 UTF-8 bytes |
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
