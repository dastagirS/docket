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

## Push validation

Every branch push runs **Validation** on native Linux x86-64 and Apple Silicon runners, using the exact pushed commit and a `dev` version stamp. New pushes cancel older validation runs on the same branch. Tag pushes do not trigger this workflow; branch deletions skip checks.

Validation runs frontend checks, Go race/vet, native executable builds and macOS signing, vulnerability scans, Linux baseline CPU/SQLite smoke checks, and real CLI/skill/installer/workflow tests. The full Go test suite runs natively, not a second time under QEMU. npm dependencies and native macOS Go caches are reused by the setup actions. Validation never packages or uploads release assets, creates tags, or publishes releases. No release credentials are passed to these checks.

Push the release commit to `master` and wait for **Validation** to pass **before** creating a release tag. Release resolves the tag to its exact commit and requires its latest push Validation run to be successful, including successful Linux/macOS jobs and required check steps from the same run attempt. Missing, pending, failed, skipped, mismatched, or unreadable validation blocks release immediately, before native build runners start. There is no polling or fallback to an older green run. The release helper does not wait for CI: atomically pushing an unvalidated branch/tag together will fail this gate; after a normal branch push has passed, the helper can push just the new tag.

## Publish a release

Use the release helper from a clean, committed `master` checkout. It needs Git and Node 24+, the existing frontend build tool—not new runtime dependencies for Docket.

```sh
./scripts/release.sh          # next patch version
./scripts/release.sh minor    # next minor version
./scripts/release.sh major    # next major version
```

The helper finds the highest stable `vMAJOR.MINOR.PATCH` tag across local Git and `origin`, including tags whose CI builds failed. It previews the next version and exact commit, then asks for confirmation (default: no). Versions are stamped from the tag by CI; no version files or commits are generated. Origin must be this Docket repository, with matching fetch/push URLs, including their resolved destinations after Git URL rewrites. The helper refuses dirty or detached checkouts, other branches, unfinished Git operations, and a remote master not contained in the local commit.

After confirmation it rechecks the checkout and origin, creates an annotated tag, and pushes the pinned commit and tag **atomically**. There is no force-push, automatic retry, non-atomic fallback, tag overwrite, or tag deletion. If a push cannot be confirmed, the new local tag is retained and the helper reports the uncertainty: inspect origin and Actions before rerunning. Failed local tags also reserve their version. An accepted push starts CI; it does not mean the release has passed or been published. Remote Git operations have a 60-second timeout, confirmation two minutes, and the whole command ten minutes; interruption can leave an uncertain push outcome.

The workflow runs automatically for `v*` tag pushes, or manually through **Actions → Release → Run workflow** in `dastagirS/docket` for an existing tag. The helper creates the tag; the workflow does not. A new two-platform release must tag source containing the updater, version stamping, and macOS support. Leave earlier tags unchanged; the current workflow does not repair older Linux-only tags.

Alternatively, once the tag exists on GitHub, use **Run workflow** and enter that tag. The workflow file must be present on the repository’s default branch for GitHub to show the manual button; keep the selected workflow branch at that default. Automatic/manual runs for the same tag share a concurrency group. These are publishing instructions, not part of the installer or local test targets.

Validation and Release share the native build recipe (`.github/workflows/checks.yml`), but checks run only on branch pushes with a `dev` stamp. After the green-commit gate, Release builds version-stamped Linux/Apple Silicon executables, signs the Mac executable, and packages/uploads the assets. It does **not** rerun Go tests/vet, frontend type checks, vulnerability scans, QEMU, or CLI/installer tests. Building assets and verifying the new Mac signature/checksums still occur because the version-stamped release binaries differ from validation builds. Checkout is pinned to the approved commit, and each platform records that source commit. Matrix fail-fast cancels the other platform after a failure; publication still requires both builds. Browser checks remain a separate local verification path because CI does not install a browser driver. Official Actions are pinned to full commit SHAs and checkout credentials are not persisted. Only the small release gate receives `actions: read` to inspect prior validation; builds retain read-only repository access.

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
DOCKET_BINARY="$build_directory/docket" node --test tests/cli.test.mjs tests/skill.test.mjs tests/release.test.mjs
sh scripts/package-release.sh v0.1.0 "$build_directory/docket" "$build_directory/release"
```

Installer tests use temporary HOME/destinations, the real built executable, and a local HTTP fixture behind a test-only curl transport rewrite. They verify checksum/error/size/redirect behavior and atomic replacement, not GitHub authentication, TLS interoperability, or hosted workflow execution. No installed CLI, existing service, project binding, or live database is modified.

Release-gate tests execute the workflow’s actual Python against a bounded local API fixture, including missing/failed/pending validation, skipped native jobs/steps, wrong commits/attempts, redirects, API errors, and oversized responses. They do not certify hosted Actions permissions or job metadata; those require the first push/release run.

Release-helper tests use disposable local bare repositories with Git restricted to the file transport. They cover version selection, confirmation, stale previews, atomic rejection, unrelated-tag exclusion, and lost/uncertain push results. They do not push to GitHub or certify GitHub permissions and hosted execution.
