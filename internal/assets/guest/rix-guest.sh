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

# Put back what an interrupted or failed swap moved aside. It is safe to run again: each
# step leaves the backup in place until its path is restored, and the journal (the plan)
# stays until every path is. It fails, keeping both, when a path can't be restored.
sync_rollback() {
    [ -f "$work/undo/plan" ] || return 0
    failed=0
    while IFS= read -r line; do
        kind=${line%% *}
        p=${line#* }
        if [ "$kind" = A ]; then
            rm -rf -- "$HOME/$p" || failed=1
        elif [ -e "$work/undo/data/$p" ] || [ -L "$work/undo/data/$p" ]; then
            if rm -rf -- "$HOME/$p" && mv "$work/undo/data/$p" "$HOME/$p"; then
                :
            else
                failed=1
            fi
        fi
    done < "$work/undo/plan"
    return "$failed"
}

# Stop unless this run still holds the lock, before each step that changes the guest.
sync_still_mine() {
    if [ "$(readlink "$HOME/.rox/sync.lock" 2>/dev/null || true)" != "$me" ]; then
        echo "Lost the sync lock" >&2
        return 1
    fi
}

sync_done() {
    status=$?
    trap - EXIT
    keep=0
    if [ -f "$work/undo/plan" ] && [ "$status" -ne 0 ]; then
        if ! sync_rollback; then
            keep=1
            echo "rix could not restore everything; the originals stay in ~/.rox/sync-work for the next sync" >&2
            [ "$status" -ne 0 ] || status=1
        fi
    fi
    if [ "$keep" = 0 ]; then
        rm -rf "$work"
    fi
    if [ "$(readlink "$HOME/.rox/sync.lock" 2>/dev/null || true)" = "$me" ]; then
        rm -f "$HOME/.rox/sync.lock"
    fi
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
        # One sync at a time. The lock is a symlink whose target, "PID:TIME", is the owner's
        # record, so creating it is one atomic step and it never exists half made. A lock is
        # stale only when its owner is gone or it is older than any sync runs. Locks an
        # older rix made were folders with a pid file inside, and are read the same way.
        me="$$:$(date +%s)"
        attempts=0
        until [ ! -d .rox/sync.lock ] && ln -s "$me" .rox/sync.lock 2>/dev/null; do
            attempts=$((attempts + 1))
            if [ "$attempts" -gt 20 ]; then
                echo "Could not take the sync lock" >&2
                exit 1
            fi
            if [ -L .rox/sync.lock ]; then
                record=$(readlink .rox/sync.lock 2>/dev/null || true)
                owner=${record%%:*}
            elif [ -d .rox/sync.lock ]; then
                owner=$(cat .rox/sync.lock/pid 2>/dev/null || true)
                record=$owner
            else
                continue
            fi
            if [ -z "$(find .rox/sync.lock -prune -mmin +10 2>/dev/null)" ] &&
                { [ -z "$owner" ] || kill -0 "$owner" 2>/dev/null; }; then
                echo "Another sync is running" >&2
                exit 1
            fi
            # Stale. Take it over by renaming it, which only one sync can do, and check that
            # what was renamed is the lock that was judged stale.
            dead=.rox/sync.lock.dead.$$
            rm -rf "$dead"
            mv .rox/sync.lock "$dead" 2>/dev/null || continue
            if [ -L "$dead" ]; then
                moved=$(readlink "$dead" 2>/dev/null || true)
            else
                moved=$(cat "$dead/pid" 2>/dev/null || true)
            fi
            if [ "$moved" != "$record" ]; then
                if [ -L "$dead" ]; then ln -s "$moved" .rox/sync.lock 2>/dev/null || true; fi
                rm -rf "$dead"
                echo "Another sync is running" >&2
                exit 1
            fi
            rm -rf "$dead"
        done
        trap sync_done EXIT
        # An earlier sync that died mid-swap leaves its undo folder: restore it first, and
        # stop if that fails, so the backups it kept survive.
        if [ -f "$work/undo/plan" ]; then
            sync_rollback || { echo "An earlier sync could not be undone; its backups are in ~/.rox/sync-work" >&2; exit 1; }
        fi
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
        sync_still_mine
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
        sync_still_mine
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
