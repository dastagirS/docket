# Releases and installation

## Supported artifacts

The release workflow builds two native executables:

- `docket-linux-amd64`: Linux x86-64, static musl and baseline `GOAMD64=v1`.
- `docket-darwin-arm64`: Apple Silicon macOS 14+, built/tested on the `macos-15` ARM64 runner. This covers M3 Macs.
- `SHA256SUMS`: one combined checksum list for both executables.

Each executable embeds the Svelte UI and SQLite. Intel macOS, Linux ARM64, and Windows are rejected rather than guessing a compatible binary. The macOS artifact is ad-hoc signed for executable integrity; it is **not Developer ID signed or notarized**, and no Apple credentials are required by CI.

`install.sh` stays in the repository root, not release assets. It detects OS/architecture (including Apple Silicon from a Rosetta shell), verifies the selected file’s checksum and ELF/Mach-O architecture, then stages and renames it on the destination filesystem. macOS also validates the code signature. Requires curl **8.4+**, `sha256sum` or `shasum`, and standard OS utilities. No Node, Docker, sudo, profile edits, or service changes occur during installation.

## Install

After the first release has been published:

```sh
curl -fsSL https://github.com/dastagirS/docket/raw/master/install.sh | sh
```

The script downloads the binary from GitHub Releases. For stricter bootstrap error handling, prefer downloading and inspecting the script before execution:

```sh
installer_directory=$(mktemp -d)
curl --proto '=https' --tlsv1.2 -fsSL \
  https://github.com/dastagirS/docket/raw/v0.1.0/install.sh -o "$installer_directory/install.sh"
# Inspect the downloaded script first, then:
DOCKET_VERSION=v0.1.0 sh "$installer_directory/install.sh"
```

Replace sample versions with an existing stable release tag. `DOCKET_VERSION` defaults to `latest`; only stable `vMAJOR.MINOR.PATCH` tags are accepted. `DOCKET_INSTALL_DIR` overrides the default `$HOME/.local/bin` and must be absolute. Add that directory to PATH yourself if necessary. Existing target symlinks/directories are refused.

Downloads are HTTPS-only (including redirects), use normal certificate verification, ignore `.curlrc`, allow at most three redirects, and have 10-second connection/120-second transfer deadlines. Checksums are capped at 16 KiB and executables at 64 MiB. The installer checks SHA-256 and the selected ELF/Mach-O header before creating destination files, stages on the destination filesystem, then atomically replaces `docket`. Failed validation/downloads leave an existing binary intact. It never executes the downloaded executable, starts a server, touches the database, or restarts an existing service. Running processes retain their old executable until explicitly restarted by the operator; take backups before any later schema upgrade.

**Checksums detect corruption; they are not independent publisher authentication.** The bootstrap script and checksum list come from the same GitHub repository/release. Trust depends on GitHub/TLS, repository access, and the release workflow. There is no detached signature/attestation scheme here. Inspect scripts and pin a release when you need a stable installation source. If `latest` changes between the two downloads, a mismatch fails closed rather than installing unchecked content. Failed cleanup or interruption after rename can leave a completed install; inspect the destination rather than assuming every nonzero exit means it is unchanged.

## Self-update

`docket update` downloads and verifies the latest stable release, then atomically replaces the invoked executable without changing data or restarting services. See [CLI self-update](CLI.md#version-and-self-update) for development-build safeguards and recovery boundaries. The installer remains in the repository root.

Release builds receive `BUILD_VERSION` from the validated release tag, passed through Make to Docker and embedded with the Go linker. Local builds default to `dev`; use `docket version` to inspect the result. The updater rejects a downloaded binary whose embedded release version differs from the selected tag.

## Publish a release

The workflow runs for `v*` tag pushes or manually through **Actions → Release → Run workflow** in `dastagirS/docket`. It requires an existing stable `vMAJOR.MINOR.PATCH` tag and never creates one. The manual form takes a required `tag`; it builds that exact tag, not the branch selected for the workflow. The first two-platform release must use a new unused tag containing the updater, version stamping, and macOS support. Leave `v0.0.1` unchanged; this workflow does not repair older Linux-only tags. Once reviewed changes are committed and pushed, an authorized maintainer can deliberately publish a new unused version:

```sh
git tag v0.1.0
git push origin v0.1.0
```

Alternatively, once the tag exists on GitHub, use **Run workflow** and enter that tag. The workflow file must be present on the repository’s default branch for GitHub to show the manual button; keep the selected workflow branch at that default. Automatic/manual runs for the same tag share a concurrency group. These are publishing instructions, not part of the installer or local test targets.

The read-only build matrix runs frontend type checks, Go race/vet tests, vulnerability scans, and CLI/skill/installer/workflow checks on Linux x86-64 and native Apple Silicon macOS. Linux additionally runs baseline QEMU/SQLite checks. Both builds carry the validated release version, and process checks verify that stamp. Browser checks remain a separate local verification path because this workflow does not install a browser driver. Official Actions are pinned to full commit SHAs and checkout credentials are not persisted.

Only the separate publish job receives `contents: write`. It downloads build artifacts without checking out/executing repository scripts or binaries, checks the manifest, and confirms the exact tag still resolves to the built commit before and after uploads. It verifies per-platform checksums and identical source commits, combines their manifests, and attaches `docket-linux-amd64`, `docket-darwin-arm64`, and `SHA256SUMS` to the release identified by GitHub’s numeric ID. The README fetches the installer from the repository root instead of release assets.

An existing release keeps its notes and matching assets; missing assets are uploaded, while conflicting/incomplete assets are rejected without overwriting them. Since tag lookup only returns published releases, a missing result triggers an authenticated draft lookup before creating anything. Lookup is bounded to ten pages of 100 releases; ambiguous matches, API errors, or an incomplete lookup fail without creating another draft. Otherwise the job creates a **draft**, uploads and verifies every file, and publishes that exact draft by ID—not by its potentially ambiguous tag. Success requires the tag’s published release to have that same ID and all three required assets with matching sizes and GitHub SHA-256 digests.

To fill missing assets for a tag that already contains the two-platform release support, start a new **Run workflow** from the updated default branch and enter that tag. Do not move/recreate the tag; **Re-run jobs** reuses the original workflow definition. A failed upload/publication may leave partial uploads or a draft: inspect release state before manually rerunning. No automatic write retries, asset deletion, or replacement are performed. GitHub manages the latest-release designation.

The scripts/workflow are local source until pushed. Actual GitHub execution and public installation cannot be certified before a tag runs successfully and its assets exist.

## Local verification

From the repository root, use an output directory separate from any running service:

```sh
build_directory=$(mktemp -d /tmp/docket-release-check-XXXXXXXX)
make test
make test-install BUILD_DIRECTORY="$build_directory"
DOCKET_BINARY="$build_directory/docket" node --test tests/cli.test.mjs tests/skill.test.mjs
sh scripts/package-release.sh v0.1.0 "$build_directory/docket" "$build_directory/release"
```

Installer tests use temporary HOME/destinations, the real built executable, and a local HTTP fixture behind a test-only curl transport rewrite. They verify checksum/error/size/redirect behavior and atomic replacement, not GitHub authentication, TLS interoperability, or hosted workflow execution. No installed CLI, existing service, project binding, or live database is modified.
