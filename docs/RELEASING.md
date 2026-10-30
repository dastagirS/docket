# Releases and installation

## Supported artifact

Linux x86-64 only, built by the existing pinned Docker toolchain: static musl, `GOAMD64=v1`, embedded Svelte UI and SQLite. Other operating systems/architectures are rejected rather than guessing a binary. Release assets are:

- `docket-linux-amd64`: the native executable, without an archive/extraction step.
- `install.sh`: the self-contained installer from this repository.
- `SHA256SUMS`: hashes of both files.

The installer requires curl **8.4+** (so streaming download size limits are enforced), sha256sum, and standard Linux utilities including `mv -T`, mktemp, awk, and od. No Node, Docker, sudo, account setup, shell-profile edits, or service changes are performed by the installer.

## Install

After the first release has been published:

```sh
installer=$(mktemp) &&
curl --proto '=https' --tlsv1.2 -fsSL https://github.com/dastagirS/docket/releases/latest/download/install.sh -o "$installer" &&
sh "$installer"
```

The chained commands preserve download failures instead of hiding them behind an empty shell pipeline. Remove the temporary bootstrap file afterward. Prefer inspecting the script before execution when practical:

```sh
installer_directory=$(mktemp -d)
curl --proto '=https' --tlsv1.2 -fsSL \
  https://github.com/dastagirS/docket/releases/download/v0.1.0/install.sh -o "$installer_directory/install.sh"
# Inspect the downloaded script first, then:
DOCKET_VERSION=v0.1.0 sh "$installer_directory/install.sh"
```

Replace sample versions with an existing stable release tag. `DOCKET_VERSION` defaults to `latest`; only stable `vMAJOR.MINOR.PATCH` tags are accepted. `DOCKET_INSTALL_DIR` overrides the default `$HOME/.local/bin` and must be absolute. Add that directory to PATH yourself if necessary. Existing target symlinks/directories are refused.

Downloads are HTTPS-only (including redirects), use normal certificate verification, ignore `.curlrc`, allow at most three redirects, and have 10-second connection/120-second transfer deadlines. Checksums are capped at 16 KiB and executables at 64 MiB. The installer checks SHA-256 and the x86-64 ELF header before creating destination files, stages on the destination filesystem, then atomically replaces `docket`. Failed validation/downloads leave an existing binary intact. It never executes the downloaded executable, starts a server, touches the database, or restarts an existing service. Running processes retain their old executable until explicitly restarted by the operator; take backups before any later schema upgrade.

**Checksums detect corruption; they are not independent publisher authentication.** The bootstrap script and checksum list come from the same GitHub repository/release. Trust depends on GitHub/TLS, repository access, and the release workflow. There is no detached signature/attestation scheme here. Inspect scripts and pin a release when you need a stable installation source. If `latest` changes between the two downloads, a mismatch fails closed rather than installing unchecked content. Failed cleanup or interruption after rename can leave a completed install; inspect the destination rather than assuming every nonzero exit means it is unchanged.

## Publish a release

The workflow runs only for `v*` tag pushes in `dastagirS/docket`, rejects non-stable tags, and does not create tags itself. Once reviewed changes are committed and pushed, an authorized maintainer can deliberately publish a new unused version:

```sh
git tag v0.1.0
git push origin v0.1.0
```

These are publishing instructions, not part of the installer or local test targets.

The read-only build job runs frontend type checks, Go race/vet tests, vulnerability scans, baseline QEMU/SQLite checks, and real CLI/skill/installer tests. Browser checks remain a separate local verification path because this workflow does not install a browser driver. Official Actions are pinned to full commit SHAs and checkout credentials are not persisted.

Only the separate publish job receives `contents: write`. It downloads the build artifacts without checking out/executing repository scripts or binaries, confirms the tag still resolves to the built commit, checks hashes, creates a **draft** with all assets, and publishes only after upload succeeds. An existing release is never clobbered. A failed upload/publication can leave a draft or an already-published release: inspect the workflow and release state before rerunning; do not automatically delete/recreate it. GitHub manages the latest-release designation.

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
