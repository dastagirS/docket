# Follow-up work

## Release checkpoint

- [x] Approve and use a pinned isolated generic Linux amd64 builder; remove host-specific AVX-512 requirements.
- [x] Audit builder/native runtime, patch the flagged Go toolchain, and pass tests, direct review, and final artifact scans. See docs/VERIFICATION.md.
- [ ] Recheck Go, SQLite, native package advisories, and resolved builder versions on future releases.

## Authentication and authorization (deferred)

V1 intentionally has no accounts, login, authenticated browser sessions, or API keys. Anyone who can reach the server can use its exposed operations; project URLs are identifiers, not access controls. Do not expose it to the public internet.

- [ ] Revisit single-owner password login and authenticated browser sessions.
- [ ] Add named, separately revocable integration API keys.
- [ ] Add project-scoped permissions, with instance-wide access explicitly enabled.
- [ ] Verify authorization across tickets, comments, history, and change events, including cross-project attempts.
