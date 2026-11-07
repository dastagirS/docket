#!/bin/sh
set -eu
script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || exit 1
command -v node >/dev/null 2>&1 || { printf 'Release requires Node 24+, the existing frontend build tool.\n' >&2; exit 1; }
exec node "$script_directory/release.mjs" "$@"
