# Security and dependency checks

Checked 2026-10-26. No known vulnerabilities were identified by the final checks below. This is a time-bound result, not a guarantee against undiscovered flaws or incomplete advisory databases.

## Deliberate v1 boundary

V1 has no accounts, sessions, API keys, or project-level authorization. Anyone who can reach the server can use its exposed operations. Keep it off the public internet and restrict network access independently. Host/Origin checks and project-scoped ticket routes prevent common mistakes, not deliberate bypass by a client with network access. Actor labels are unverified. Authentication/authorization remain in TODO.md.

## Application and native runtime

- Application module: `github.com/mattn/go-sqlite3 v1.14.52`; SQLite 3.53.4. No transitive Go modules. Go module checksum verification passed.
- Builder: `golang:1.27.3-alpine3.24@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c`, selected for Linux amd64.
- Compiler/native runtime pins: GCC 15.2.0-r5, musl and musl-dev 1.2.6-r3, binutils 2.45.1-r1. The Alpine r3 musl package includes published security fixes missing in earlier revisions.
- Builder updates installed before compilation; the resolved 31-package inventory was compared with Alpine v3.24 main's security feed using 184 `apk version` comparisons. Zero installed versions older than the listed fix versions were found. This is feed-based comparison, not an independent whole-toolchain source audit.
- Final `govulncheck v1.9.0` module, package (including tests/build tags), and **binary** scans all passed. Go scanners do not independently analyze SQLite C or musl. SQLite's published CVE table, release information, upstream advisories, and the native package feed were reviewed separately.

The first generic builder used Go 1.27.2. Its binary scan flagged GO-2026-6809 / CVE-2026-111943 in `crypto/x509.Certificate.Verify`, despite clean source scans. The builder and minimum Go requirement were raised to 1.27.3; the final binary scan passed. The flagged binary is not the final artifact.

The base-image digest freezes the base, not external Alpine repositories. `apk upgrade` intentionally applies current signed repository updates, and explicit compiler/musl versions fail if unavailable. Capture and review resolved versions on every release and rerun native advisory checks whenever packages change. `make security` repeats Go checks; it does not replace native dependency review.

## Frontend migration

The UI now uses Svelte 5, TypeScript, and Vite, pinned in `frontend/package.json` and `frontend/package-lock.json`. The Node 24 build image is digest-pinned in Dockerfile. Node/npm are not present in the final scratch runtime. `npm --prefix frontend audit` reported zero known vulnerabilities for the locked tree during migration; rerun it for future releases. The Go scanner does not cover this dependency tree.

Production assets remain same-origin under `/assets/`, with no `unsafe-inline` or `unsafe-eval` CSP exception. Browser checks load the actual Svelte production bundle, SVG sprite, and locally embedded Manrope font under Go's CSP. User content is rendered as text, not raw HTML. Vite's loopback-only development proxy rewrites Host/Origin for the local Go backend; it is not a production security boundary.

## Portability and deployment

The original host-built binary declared x86-64-v4/AVX-512 requirements inherited from the host's optimized glibc. The final builder uses `GOAMD64=v1`, generic x86-64 C flags, and static musl. ELF inspection found no above-baseline ISA requirement or dynamic interpreter. Baseline QEMU `qemu64` execution passed a real SQLite backup and all 14 API/store tests. This is executable compatibility evidence, not a claim of testing every physical CPU/kernel. Only Linux amd64 is shipped.

The scratch runtime image adds no runtime OS packages, but its executable still includes native runtime code. It runs as UID/GID 65532. Verification also used a read-only root filesystem, dropped capabilities, no-new-privileges, a 128 MiB memory limit, and a persistent local named volume. Project/ticket events survived container removal and recreation. No public deployment was performed.

## Evidence and remaining limits

See VERIFICATION.md. No production load campaign, power-cut/disk-failure guarantee, full native source audit, independent penetration test, or other-architecture verification is claimed. WAL/backup files need disk headroom beyond the main database cap. Keep independent backups and review future advisories.

Public references:

- https://vuln.go.dev/ID/GO-2026-6809.json
- https://secdb.alpinelinux.org/v3.24/main.json
- https://sqlite.org/cves.html
- https://sqlite.org/releaselog/3_53_4.html
- https://github.com/mattn/go-sqlite3/security/advisories

## Local CLI bindings

The private `~/.docket/registry.json` maps canonical folders to server/projects; it is not authorization or Pi ownership. Registry metadata is stored in bounded regular owner-only files; symlinks and malformed metadata are rejected. Identity verification compares the project key and creation timestamp before implicit ticket requests. Pending `~/.docket/init.lock` blocks affected implicit scope rather than guessing or replaying an interrupted init. Keep the user registry/journal private and out of source control; init never changes external integration pins. Filesystem durability is best-effort with sync/atomic rename, not a power-loss or hostile same-user process guarantee.
