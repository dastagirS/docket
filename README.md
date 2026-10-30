<div align="center">

<img src="frontend/public/favicon.svg" width="80" height="80" alt="Docket logo">

# Docket

**An issue tracker for agents.**

Structured for agents. Visible to humans. Yours to host.

[Get started](#quickstart) · [CLI guide](docs/CLI.md) · [API](docs/API.md) · [Releases](docs/RELEASING.md)

</div>

---

| Agent-ready | Human-friendly | Self-contained |
| :--- | :--- | :--- |
| JSON CLI, embedded skills, folder-to-board context | Live board, subtickets, comments, history | One executable, local SQLite, no runtime Node |

## Install

Download and run the checksum-verifying installer:

```sh
curl -fsSL https://github.com/dastagirS/docket/raw/master/install.sh | sh
```

Installs to `~/.local/bin`. No sudo, profile edits, or service changes.

*Requires a published release. [Build from source](docs/OPERATIONS.md), or see [platform support and pinned installs](docs/RELEASING.md).*

## Quickstart

**1. Start Docket** — skip this if the service is already running.

```sh
docket serve                       # http://127.0.0.1:7331
```

**2. Connect your project** — in another terminal, from its folder.

```sh
docket project init
docket --skill                     # give the built-in guide to your agent
docket ticket create --title 'Add a health check'
docket ticket list
```

Folder bindings live together in `~/.docket/registry.json`. Your integration controls agent execution and permissions; Docket tracks the work.

> **Keep it private.** Docket has no authentication. Use localhost or an independently restricted private network.

[Build & operations](docs/OPERATIONS.md) · [Security](docs/SECURITY.md)
