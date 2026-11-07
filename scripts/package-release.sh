#!/bin/sh
set -eu

fail() {
    printf 'release packaging: %s\n' "$1" >&2
    exit 1
}

[ "$#" -eq 3 ] || fail 'Usage: scripts/package-release.sh vMAJOR.MINOR.PATCH BINARY NEW_OUTPUT_DIRECTORY'
version=$1
binary=$2
output_directory=$3
version_bytes_max=64
[ "${#version}" -le "$version_bytes_max" ] || fail 'Release tag is too long.'
case "$version" in *[!v0-9.]*) fail 'Invalid release tag.' ;; esac
printf '%s\n' "$version" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || fail 'Use a stable vMAJOR.MINOR.PATCH tag.'
[ -f "$binary" ] && [ ! -L "$binary" ] && [ -s "$binary" ] || fail 'Build a regular, nonempty executable first.'
binary_bytes_max=67108864
binary_bytes=$(wc -c < "$binary") || fail 'Cannot inspect executable size.'
[ "$binary_bytes" -le "$binary_bytes_max" ] || fail 'Binary exceeds installer download limit.'
case "$output_directory" in /*) ;; *) fail 'Output directory must be absolute.' ;; esac
[ ! -e "$output_directory" ] && [ ! -L "$output_directory" ] || fail 'Output directory must not already exist; releases are not overwritten.'
header_bytes=8
machine_offset_bytes=18
machine_size_bytes=2
signature=$(od -An -N "$header_bytes" -tx1 "$binary" | tr -d '[:space:]') || fail 'Cannot identify executable format.'
case "$signature" in
    7f454c460201*)
        machine=$(od -An -j "$machine_offset_bytes" -N "$machine_size_bytes" -tx1 "$binary" | tr -d '[:space:]') || fail 'Cannot inspect Linux CPU architecture.'
        [ "$machine" = 3e00 ] || fail 'Linux releases require an x86-64 executable.'
        asset=docket-linux-amd64 ;;
    cffaedfe0c000001) asset=docket-darwin-arm64 ;;
    *) fail 'Only Linux x86-64 ELF and Apple Silicon Mach-O assets are supported.' ;;
esac
mkdir -- "$output_directory" || fail 'Cannot create release output directory.'
cp -- "$binary" "$output_directory/$asset" || fail 'Cannot package executable.'
chmod 755 "$output_directory/$asset" || fail 'Cannot set release file permissions.'
(cd -- "$output_directory" && if command -v sha256sum >/dev/null 2>&1; then sha256sum "$asset"; else shasum -a 256 "$asset"; fi > SHA256SUMS) || fail 'Cannot generate release checksums.'
printf 'Packaged %s (%s) in %s\n' "$version" "$asset" "$output_directory"
