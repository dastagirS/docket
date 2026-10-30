<p><img src="frontend/public/favicon.svg" width="64" height="64" alt="Docket Queue logo"></p>

# Docket

**An issue tracker for agents.** A CLI for agents, a board for humans. One binary, local SQLite, no runtime Node dependency.

- JSON output, embedded agent skills, and project context via `.docket`.
- Version-safe updates, subtickets, comments, and durable history.
- A live-updating web board for reviewing the same work.

## Install

Linux x86-64, curl 8.4+. Requires a published release; other platforms are not shipped yet.

```sh
installer=$(mktemp) &&
curl --proto '=https' --tlsv1.2 -fsSL https://github.com/dastagirS/docket/releases/latest/download/install.sh -o "$installer" &&
sh "$installer"
```

Installs to `~/.local/bin` after SHA-256 verification. No sudo or service changes. [Inspect the installer](install.sh) or [build from source](docs/OPERATIONS.md).

## Quickstart

```sh
docket serve                       # skip if already running; http://127.0.0.1:7331
```

In another terminal, from your project folder:

```sh
docket project init
docket --skill                     # give this guide to your agent
docket ticket create --title 'Add a health check'
docket ticket list
```

Docket tracks work; your integration controls agent execution and permissions.

**No authentication: keep Docket on localhost or an independently restricted private network.**

[CLI](docs/CLI.md) · [API](docs/API.md) · [Build & operations](docs/OPERATIONS.md) · [Releases](docs/RELEASING.md) · [Security](docs/SECURITY.md)
