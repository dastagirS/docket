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
script_directory=$(dirname -- "$0") || fail 'Cannot locate packaging script.'
repository_directory=$(CDPATH='' cd -- "$script_directory/.." && pwd) || fail 'Cannot locate repository sources.'
mkdir -- "$output_directory" || fail 'Cannot create release output directory.'
cp -- "$binary" "$output_directory/docket-linux-amd64" || fail 'Cannot package executable.'
cp -- "$repository_directory/install.sh" "$output_directory/install.sh" || fail 'Cannot package installer.'
chmod 755 "$output_directory/docket-linux-amd64" "$output_directory/install.sh" || fail 'Cannot set release file permissions.'
(cd -- "$output_directory" && sha256sum docket-linux-amd64 install.sh > SHA256SUMS) || fail 'Cannot generate release checksums.'
printf 'Packaged %s for Linux x86-64 in %s\n' "$version" "$output_directory"
