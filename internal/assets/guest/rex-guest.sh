#!/usr/bin/env bash
# rox-rex's guest side. rox-rex sends it through rox exec. It installs Rex when the guest has
# none and starts the guest's own Rex server; rox itself never needs either.
#
#   rex-guest.sh install CHANNEL       install Rex from the release channel unless it's there
#   rex-guest.sh server SOCKET         start the guest's Rex server at SOCKET unless it's running
set -euo pipefail
export PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"

action=$1
shift

hook_env() {
    # Shells read ~/.rox/NAME.env, which this script writes.
    local line="[ -f \"\$HOME/.rox/$1.env\" ] && . \"\$HOME/.rox/$1.env\"" file
    for file in "$HOME/.profile" "$HOME/.bashrc" "$HOME/.zshenv"; do
        grep -qxF "$line" "$file" 2>/dev/null || printf '%s\n' "$line" >>"$file"
    done
}

case "$action" in
    install)
        if command -v rex >/dev/null; then
            rex version | head -n 1
            exit 0
        fi
        if [[ $(uname -s) != Linux ]]; then
            echo 'Install the Rex app in the guest, or prepare its template with rox template --with-rex.' >&2
            exit 3
        fi
        installer=$(mktemp)
        trap 'rm -f "$installer"' EXIT
        curl -fsSL https://pkgs.superlogical.com/rex/install.sh -o "$installer"
        S=$(test "$(id -u)" = 0 || echo sudo)
        $S sh "$installer" --tarball --channel "$1" >&2
        if ! command -v rex >/dev/null; then
            found=$($S sh -lc 'command -v rex' || true)
            [[ -n $found ]] && $S install -m 0755 "$found" /usr/local/bin/rex
        fi
        rex version | head -n 1
        ;;
    server)
        socket=$1
        base="${socket%/*}"
        # Rox forwards the launcher's Git keys to this path, when the guest has them.
        export SSH_AUTH_SOCK="$base/git-agent.sock"
        endpoint="unix://$socket"
        command -v rex >/dev/null || { echo 'Rex isn'\''t installed in the guest.' >&2; exit 1; }
        # Rex doesn't tell the guest's terminals which server they're in, so rex there would look
        # for a server at the default socket. The server and every shell get this one.
        mkdir -p "$HOME/.rox"
        printf '[ -n "${REX_SERVER:-}" ] || export REX_SERVER=%q\n' "$endpoint" >"$HOME/.rox/rex.env"
        hook_env rex
        rex_ready() {
            [[ -S $1 ]] && rex --autostart=false --timeout 1s -S "$endpoint" ls >/dev/null 2>&1
        }
        if rex_ready "$socket"; then
            exit 0
        fi
        log="$base/rex.log"
        # The server, and every terminal it opens, gets the agents' credentials.
        if [[ -f $HOME/.rox/agents.env ]]; then
            . "$HOME/.rox/agents.env"
        fi
        export REX_SERVER="$endpoint"
        nohup rex server run --listen "$endpoint" --state-dir "$base/rex-state" >"$log" 2>&1 </dev/null &
        server_pid=$!
        deadline=$((SECONDS + 10))
        while ((SECONDS < deadline)); do
            if rex_ready "$socket"; then
                exit 0
            fi
            kill -0 "$server_pid" 2>/dev/null || break
            sleep 0.2
        done
        echo "Rex did not answer inside the guest at $socket." >&2
        cat "$log" >&2
        exit 1
        ;;
    *)
        echo "Unknown action $action" >&2
        exit 2
        ;;
esac
