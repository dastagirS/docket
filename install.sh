#!/bin/sh
set -eu

fail() {
    printf 'docket installer: %s\n' "$1" >&2
    exit 1
}

cleanup() {
    status=$?
    trap - 0
    if [ -n "$staging_file" ] && ! rm -f -- "$staging_file"; then
        printf 'Cannot remove installer staging file\n' >&2
        status=1
    fi
    if [ -n "$temporary_directory" ] && ! rm -rf -- "$temporary_directory"; then
        printf 'Cannot remove installer temporary directory\n' >&2
        status=1
    fi
    exit "$status"
}

download() {
    if ! curl --disable --fail --silent --show-error --location \
        --proto '=https' --proto-redir '=https' --tlsv1.2 \
        --connect-timeout "$connection_timeout_seconds" --max-time "$download_timeout_seconds" \
        --max-redirs "$redirects_max" --max-filesize "$3" --output "$2" "$1"; then
        fail 'Download failed. No existing installation was changed.'
    fi
}

main() {
    if [ "$#" -eq 1 ] && { [ "$1" = '--help' ] || [ "$1" = '-h' ]; }; then
        printf '%s\n' 'Install Docket for Linux x86-64 or Apple Silicon macOS.' \
            'Usage: sh install.sh' \
            'DOCKET_VERSION: latest (default) or a stable tag such as v0.1.0' \
            'DOCKET_INSTALL_DIR: absolute destination directory (default: $HOME/.local/bin)' \
            'Requires curl 8.4+, sha256sum or shasum, and standard system utilities.' \
            'Verifies release checksums; never runs Docket, uses sudo, or changes services.'
        return
    fi
    [ "$#" -eq 0 ] || fail 'Unexpected arguments; use --help.'
    version=${DOCKET_VERSION:-latest}
    version_bytes_max=64
    [ "${#version}" -le "$version_bytes_max" ] || fail 'Release tag is too long.'
    if [ "$version" != latest ]; then
        case "$version" in *[!v0-9.]*) fail 'Invalid release tag.' ;; esac
        printf '%s\n' "$version" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || fail 'Use latest or a stable vMAJOR.MINOR.PATCH tag.'
    fi
    for utility in uname curl awk grep mktemp od tr cat chmod mkdir mv rm; do
        command -v "$utility" >/dev/null 2>&1 || fail "Required utility is missing: $utility"
    done
    operating_system=$(uname -s) || fail 'Cannot determine the operating system.'
    architecture=$(uname -m) || fail 'Cannot determine the CPU architecture.'
    case "$operating_system/$architecture" in
        Linux/x86_64|Linux/amd64) asset=docket-linux-amd64 ;;
        Darwin/arm64|Darwin/aarch64) asset=docket-darwin-arm64 ;;
        Darwin/x86_64)
            command -v sysctl >/dev/null 2>&1 || fail 'Cannot identify Apple Silicon hardware.'
            [ "$(sysctl -n hw.optional.arm64 2>/dev/null)" = 1 ] || fail 'Intel macOS is not supported.'
            asset=docket-darwin-arm64 ;;
        *) fail 'Supported releases: Linux x86-64 and Apple Silicon macOS.' ;;
    esac
    if command -v sha256sum >/dev/null 2>&1; then checksum_command=sha256sum
    elif command -v shasum >/dev/null 2>&1; then checksum_command=shasum
    else fail 'SHA-256 verification requires sha256sum or shasum.'; fi
    if [ "$operating_system" = Darwin ]; then command -v codesign >/dev/null 2>&1 || fail 'macOS signature validation requires codesign.'; fi
    if [ -n "${DOCKET_INSTALL_DIR:-}" ]; then
        install_directory=$DOCKET_INSTALL_DIR
    else
        [ -n "${HOME:-}" ] || fail 'Set HOME or DOCKET_INSTALL_DIR.'
        install_directory=$HOME/.local/bin
    fi
    case "$install_directory" in /*) ;; *) fail 'DOCKET_INSTALL_DIR must be absolute.' ;; esac
    directory_without_controls=$(printf '%s' "$install_directory" | LC_ALL=C tr -d '[:cntrl:]') || fail 'Cannot validate installation directory.'
    [ "$directory_without_controls" = "$install_directory" ] || fail 'Installation directory must not contain control characters.'
    curl_information=$(curl --disable --version) || fail 'Cannot inspect curl version.'
    # Older curl releases only enforce --max-filesize when Content-Length is known.
    printf '%s\n' "$curl_information" | awk 'NR == 1 { split($2, version, "[.]"); supported=(version[1]+0 > 8 || (version[1]+0 == 8 && version[2]+0 >= 4)) } END { exit !supported }' || fail 'curl 8.4 or newer is required for bounded streaming downloads.'
    target=$install_directory/docket
    [ ! -L "$target" ] || fail 'Destination is a symlink; choose another installation directory.'
    [ ! -e "$target" ] || [ -f "$target" ] || fail 'Destination is not a regular file.'
    repository=https://github.com/dastagirS/docket
    if [ "$version" = latest ]; then
        release_url=$repository/releases/latest/download
    else
        release_url=$repository/releases/download/$version
    fi
    connection_timeout_seconds=10
    download_timeout_seconds=120
    redirects_max=3
    checksum_bytes_max=16384
    binary_bytes_max=67108864
    checksum_hex_length=64
    identification_size_bytes=6
    machine_offset_bytes=18
    machine_size_bytes=2
    temporary_directory=''
    staging_file=''
    umask 077
    temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/docket-install.XXXXXXXX") || fail 'Cannot create a private temporary directory.'
    trap cleanup 0
    trap 'exit 1' HUP INT TERM
    download "$release_url/SHA256SUMS" "$temporary_directory/SHA256SUMS" "$checksum_bytes_max"
    expected=$(awk -v asset="$asset" -v checksum_length="$checksum_hex_length" '
        $2 == asset {
            count++
            if (NF != 2 || length($1) != checksum_length || $1 ~ /[^0-9a-f]/) invalid=1
            checksum=$1
        }
        END { if (count != 1 || invalid) exit 1; print checksum }
    ' "$temporary_directory/SHA256SUMS") || fail 'Missing, duplicated, or malformed binary checksum.'
    download "$release_url/$asset" "$temporary_directory/$asset" "$binary_bytes_max"
    if [ "$checksum_command" = sha256sum ]; then
        actual=$(sha256sum < "$temporary_directory/$asset") || fail 'Cannot calculate the binary checksum.'
    else
        actual=$(shasum -a 256 < "$temporary_directory/$asset") || fail 'Cannot calculate the binary checksum.'
    fi
    actual=${actual%% *}
    [ "$actual" = "$expected" ] || fail 'SHA-256 mismatch. No existing installation was changed.'
    if [ "$asset" = docket-linux-amd64 ]; then
        signature=$(od -An -N "$identification_size_bytes" -tx1 "$temporary_directory/$asset") || fail 'Cannot inspect the binary header.'
        signature=$(printf '%s' "$signature" | tr -d '[:space:]')
        machine=$(od -An -j "$machine_offset_bytes" -N "$machine_size_bytes" -tx1 "$temporary_directory/$asset") || fail 'Cannot inspect the binary architecture.'
        machine=$(printf '%s' "$machine" | tr -d '[:space:]')
        [ "$signature" = 7f454c460201 ] && [ "$machine" = 3e00 ] || fail 'Release asset is not a Linux x86-64 ELF binary.'
    else
        macho_header_bytes=16
        signature=$(od -An -N "$macho_header_bytes" -tx1 "$temporary_directory/$asset") || fail 'Cannot inspect the Mach-O header.'
        signature=$(printf '%s' "$signature" | tr -d '[:space:]')
        case "$signature" in cffaedfe0c000001????????02000000) ;; *) fail 'Release asset is not an Apple Silicon Mach-O executable.' ;; esac
        codesign --verify --strict "$temporary_directory/$asset" || fail 'macOS release signature validation failed.'
    fi
    mkdir -p -- "$install_directory" || fail 'Cannot create installation directory; no sudo is used.'
    staging_file=$(mktemp "$install_directory/.docket-install.XXXXXXXX") || fail 'Cannot stage the executable in the destination directory.'
    cat "$temporary_directory/$asset" > "$staging_file" || fail 'Cannot copy the verified executable.'
    chmod 755 "$staging_file" || fail 'Cannot set executable permissions.'
    # Same-directory rename keeps a running executable intact and prevents partial upgrades.
    [ ! -L "$target" ] && { [ ! -e "$target" ] || [ -f "$target" ]; } || fail 'Destination changed before replacement.'
    if [ "$operating_system" = Darwin ]; then
        mv -fh -- "$staging_file" "$target" || fail 'Cannot atomically replace the executable.'
    else
        mv -fT -- "$staging_file" "$target" || fail 'Cannot atomically replace the executable.'
    fi
    [ -f "$target" ] && [ ! -L "$target" ] || fail 'Destination changed during installation; inspect it before retrying.'
    staging_file=''
    printf 'Installed Docket (%s) to %s\n' "$version" "$target"
    case ":${PATH:-}:" in *":$install_directory:"*) ;; *) printf 'Add %s to your PATH.\n' "$install_directory" ;; esac
    printf 'No service was started or restarted. Run docket --help to get started.\n'
}

# Define the complete installer before running it, so a truncated piped download cannot run a partial body.
main "$@"
