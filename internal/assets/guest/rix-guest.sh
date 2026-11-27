#!/bin/sh
# rix's guest side. rix sends it through rox exec with each secret on stdin, so no secret
# appears in argv or in a host process's environment. It only writes credential files and
# unpacks synced agent setup; it installs nothing.
set -eu
export PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"

# Marks that rix, not the guest's own login, wrote Codex's credential.
mark_codex() {
    umask 077
    mkdir -p "$HOME/.rox"
    : > "$HOME/.rox/rix-codex-login"
}

# A path a sync may touch: relative, inside the home folder.
safe_path() {
    case "$1" in
        ""|/*|..|../*|*/..|*/../*) return 1 ;;
    esac
    return 0
}

work="$HOME/.rox/sync-work"

# Put back what an interrupted or failed swap moved aside.
sync_rollback() {
    [ -f "$work/undo/plan" ] || return 0
    while IFS= read -r line; do
        kind=${line%% *}
        p=${line#* }
        if [ "$kind" = A ]; then
            rm -rf -- "$HOME/$p"
        elif [ -e "$work/undo/data/$p" ] || [ -L "$work/undo/data/$p" ]; then
            rm -rf -- "$HOME/$p"
            mv "$work/undo/data/$p" "$HOME/$p"
        fi
    done < "$work/undo/plan"
}

sync_done() {
    status=$?
    trap - EXIT
    if [ "$status" -ne 0 ]; then
        sync_rollback || true
    fi
    rm -rf "$work"
    rm -rf "$HOME/.rox/sync.lock"
    exit "$status"
}

action=$1
shift
case "$action" in
    env)
        # Shells and the guest's Rex server read this file, which rox's bootstrap sources.
        umask 077
        mkdir -p "$HOME/.rox"
        cat > "$HOME/.rox/agents.env.tmp"
        mv "$HOME/.rox/agents.env.tmp" "$HOME/.rox/agents.env"
        ;;
    codex-auth)
        umask 077
        mkdir -p "$HOME/.codex"
        cat > "$HOME/.codex/auth.json.tmp"
        mv "$HOME/.codex/auth.json.tmp" "$HOME/.codex/auth.json"
        mark_codex
        ;;
    codex-login)
        codex login --with-api-key >/dev/null
        mark_codex
        ;;
    codex-forget)
        # Sign Codex out only when rix signed it in: the marker rix leaves, or the launcher's
        # record that an older rix did ("owned"). A login made in the guest stays.
        if [ -f "$HOME/.rox/rix-codex-login" ] || [ "${1:-}" = owned ]; then
            rm -f "$HOME/.codex/auth.json" "$HOME/.codex/auth.json.tmp" "$HOME/.rox/rix-codex-login"
        fi
        ;;
    read)
        # Each file that exists, as its path and its contents in base64 on one line.
        cd "$HOME"
        for file; do
            if [ -f "$file" ]; then
                printf '%s ' "$file"
                base64 < "$file" | tr -d '\n'
                echo
            fi
        done
        ;;
    sync)
        # Arguments: the paths to install, then --, then stale paths to remove afterwards.
        # The tar on stdin is unpacked into a staging folder and checked first. Only then does
        # each path swap in, the old one kept aside so a failure puts it back.
        cd "$HOME"
        mkdir -p .rox
        # One sync at a time. A lock whose owner died is taken over.
        if ! mkdir .rox/sync.lock 2>/dev/null; then
            owner=$(cat .rox/sync.lock/pid 2>/dev/null || true)
            if [ -n "$owner" ] && kill -0 "$owner" 2>/dev/null; then
                echo "Another sync is running" >&2
                exit 1
            fi
            rm -rf .rox/sync.lock
            mkdir .rox/sync.lock
        fi
        echo $$ > .rox/sync.lock/pid
        trap sync_done EXIT
        # An earlier sync that died mid-swap leaves its undo folder: restore it first.
        sync_rollback
        rm -rf "$work"
        mkdir "$work" "$work/stage" "$work/undo"
        : > "$work/install.list"
        : > "$work/stale.list"
        list=install
        for p; do
            if [ "$p" = -- ]; then
                list=stale
                continue
            fi
            safe_path "$p" || { printf 'Unsafe sync path %s\n' "$p" >&2; exit 1; }
            printf '%s\n' "$p" >> "$work/$list.list"
        done
        (cd "$work/stage" && tar -xf -)
        # The sender ends the tar with this file, so a short transfer fails here.
        [ -f "$work/stage/.rix-complete" ] || { echo "The sync archive was cut short" >&2; exit 1; }
        rm -f "$work/stage/.rix-complete"
        while IFS= read -r p; do
            if [ ! -e "$work/stage/$p" ] && [ ! -L "$work/stage/$p" ]; then
                printf 'The sync archive lacks %s\n' "$p" >&2
                exit 1
            fi
        done < "$work/install.list"
        # Plan the swap before touching anything: E for a path that exists, A for one that doesn't.
        : > "$work/undo/plan.tmp"
        while IFS= read -r p; do
            if [ -e "$p" ] || [ -L "$p" ]; then
                printf 'E %s\n' "$p" >> "$work/undo/plan.tmp"
            else
                printf 'A %s\n' "$p" >> "$work/undo/plan.tmp"
            fi
        done < "$work/install.list"
        mv "$work/undo/plan.tmp" "$work/undo/plan"
        while IFS= read -r p; do
            mkdir -p "$(dirname "$p")"
            if [ -e "$p" ] || [ -L "$p" ]; then
                mkdir -p "$work/undo/data/$(dirname "$p")"
                mv "$p" "$work/undo/data/$p"
            fi
            mv "$work/stage/$p" "$p"
        done < "$work/install.list"
        # The new setup is in place: now drop what the launcher no longer syncs.
        while IFS= read -r p; do
            rm -rf -- "$p"
        done < "$work/stale.list"
        ;;
    forget)
        rm -f "$HOME/.rox/agents.env"
        ;;
    *)
        printf 'Unknown rix guest action %s\n' "$action" >&2
        exit 1
        ;;
esac
