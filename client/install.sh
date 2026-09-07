#!/bin/sh
# Download as the caller; elevate only the verified local system installer.
# Keep the whole program in a function so a truncated pipe cannot run its prefix.
onecat_install() (
    set -eu
    umask 077
    base='https://github.com/1CatAI/1Cat-Tunnel/releases/download/client-v0.5.2-security-r2'
    archive='1cattunnel-linux-amd64-0.5.2-security-r2.tar.gz'
    expected='6165b93cdba1832dbab9e027f4261368ded9df889cbfb1b28783516b69249156'
    yes=0
    mode=system
    config=
    output=
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --yes) yes=1 ;;
            --user) mode=user ;;
            --download-only) mode=download ;;
            --config) shift; config="${1:?Missing absolute config path}" ;;
            --output) shift; output="${1:?Missing output directory}" ;;
            --help) echo 'Usage: sh install.sh --yes [--user | --download-only] [--config FILE] [--output DIR]'; exit 0 ;;
            *) echo "Unknown option: $1" >&2; exit 2 ;;
        esac
        shift
    done
    [ "$(uname -s)" = Linux ] || { echo 'Linux is required.' >&2; exit 1; }
    case "$(uname -m)" in x86_64|amd64) ;; *) echo 'This release supports Linux x86_64/amd64 only.' >&2; exit 1 ;; esac
    for tool in curl sha256sum tar mktemp stat readlink; do
        command -v "$tool" >/dev/null 2>&1 || { echo "Required system utility missing: $tool" >&2; exit 1; }
    done
    [ -z "$config" ] || case "$config" in /*) ;; *) echo '--config must be absolute.' >&2; exit 1 ;; esac
    case "$config" in *"'"*|*'
'*) echo 'Unsupported config path characters.' >&2; exit 1 ;; esac
    if [ "$mode" = system ]; then
        command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] || { echo 'systemd is required; use --user for a non-persistent installation.' >&2; exit 1; }
        if [ "$(id -u)" -ne 0 ]; then command -v sudo >/dev/null 2>&1 || { echo 'sudo is required; use --user otherwise.' >&2; exit 1; }; fi
    fi
    if [ "$mode" = user ]; then
        [ "$(id -u)" -ne 0 ] || { echo '--user must be run without sudo/root.' >&2; exit 1; }
    fi
    if [ "$mode" != download ] && [ "$yes" -ne 1 ]; then
        echo 'Installation is explicit. Use --yes; default mode installs/starts a systemd service.' >&2
        exit 1
    fi
    # The original service owner must be able to traverse this directory after sudo.
    case "$HOME" in /*) ;; *) echo 'HOME must be absolute.' >&2; exit 1 ;; esac
    work="$(mktemp -d "$HOME/.1cattunnel-install.XXXXXXXX")"
    trap 'rm -rf -- "$work"' EXIT
    trap 'exit 130' INT
    trap 'exit 143' HUP TERM
    echo 'Downloading 1cattunnel from the official GitHub Release...'
    curl --disable --fail --location --silent --show-error \
        --proto '=https' --proto-redir '=https' --tlsv1.2 \
        --connect-timeout 15 --max-time 300 --retry 2 --max-filesize 33554432 \
        "$base/$archive" -o "$work/$archive"
    printf '%s  %s\n' "$expected" "$work/$archive" | sha256sum -c -
    if [ "$mode" = download ]; then
        output="${output:-$PWD}"
        [ -d "$output" ] || { echo 'Output directory must already exist.' >&2; exit 1; }
        [ ! -e "$output/$archive" ] && [ ! -L "$output/$archive" ] || { echo 'Refusing to overwrite an existing output.' >&2; exit 1; }
        (set -C; cat "$work/$archive" > "$output/$archive")
        echo "Verified archive: $output/$archive"
        exit 0
    fi
    tar -xzf "$work/$archive" -C "$work" --no-same-owner
    package="$work/linux-client-delivery"
    binary="$package/1cat-tunnel-client"
    [ -x "$binary" ] || { echo 'The verified archive has no executable client.' >&2; exit 1; }
    "$binary" -version
    if [ "$mode" = system ]; then
        if [ "$(id -u)" -eq 0 ]; then
            sh "$package/install-client-systemd.sh" --yes ${config:+"$config"}
        else
            sudo sh "$package/install-client-systemd.sh" --yes ${config:+"$config"}
        fi
        exit 0
    fi
    # Optional trials never register a service or modify a running client.
    case "$HOME" in *"'"*|*'
'*) echo 'Unsupported HOME characters.' >&2; exit 1 ;; esac
    uid="$(id -u)"
    trusted() {
        p="$1"
        while [ "$p" != / ]; do
            [ ! -L "$p" ] || { echo "Refusing linked install path: $p" >&2; exit 1; }
            if [ -e "$p" ]; then
                owner="$(stat -c %u -- "$p")"; permissions="$(stat -c %a -- "$p")"
                { [ "$owner" = "$uid" ] || [ "$owner" = 0 ]; } && [ "$((0$permissions & 18))" -eq 0 ] || { echo "Unsafe install path: $p" >&2; exit 1; }
            fi
            p="$(dirname -- "$p")"
        done
    }
    root="$HOME/.local/lib/1cattunnel-github"
    bindir="$HOME/.local/bin"
    trusted "$root"
    trusted "$bindir"
    if [ -e "$bindir/1cattunnel" ] || [ -L "$bindir/1cattunnel" ]; then
        [ -L "$bindir/1cattunnel" ] && [ "$(readlink "$bindir/1cattunnel")" = "$root/current/1cattunnel" ] || { echo 'Existing 1cattunnel command belongs to another installer; refusing to replace it.' >&2; exit 1; }
    fi
    mkdir -p "$root/releases" "$bindir"
    versiondir="$(mktemp -d "$root/releases/0.5.2-security-r2.XXXXXXXX")"
    cp "$binary" "$versiondir/tunnel-client"
    chmod 0755 "$versiondir/tunnel-client"
    config="${config:-$HOME/.config/1cat-tunnel/client-linux.json}"
    "$versiondir/tunnel-client" -initialize-config -config "$config"
    ca="$(dirname -- "$config")/1cat-tunnel-ca.pem"
    [ ! -L "$ca" ] || { echo 'Refusing linked CA file.' >&2; exit 1; }
    if [ -e "$ca" ]; then
        [ -f "$ca" ] && [ -r "$ca" ] || { echo 'Existing CA is not a readable file.' >&2; exit 1; }
    else
        (set -C; cat "$package/1cat-tunnel-ca.pem" > "$ca")
    fi
    cat > "$versiondir/1cattunnel" <<ONECAT_LAUNCHER
#!/bin/sh
case "\${1:-}" in
    panel) shift; exec '$versiondir/tunnel-client' -panel -config '$config' "\$@" ;;
    status) shift; exec '$versiondir/tunnel-client' -status -config '$config' "\$@" ;;
    --version|-version) exec '$versiondir/tunnel-client' -version ;;
    *) exec '$versiondir/tunnel-client' -config '$config' "\$@" ;;
esac
ONECAT_LAUNCHER
    chmod 0755 "$versiondir/1cattunnel"
    ln -s "$versiondir" "$root/current.new.$$"
    mv -Tf "$root/current.new.$$" "$root/current"
    ln -s "$root/current/1cattunnel" "$bindir/1cattunnel.new.$$"
    mv -Tf "$bindir/1cattunnel.new.$$" "$bindir/1cattunnel"
    echo "Installed: $bindir/1cattunnel"
    echo 'No service was installed. Run that command to open the local management panel.'
    echo 'Previous binaries and configuration were retained.'
)
onecat_install "$@"
