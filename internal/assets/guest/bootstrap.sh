#!/usr/bin/env bash
set -euo pipefail

export PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"

stage=$1
shift

hook_env() {
    # Shells read ~/.rox/NAME.env, which rox writes at boot.
    local line="[ -f \"\$HOME/.rox/$1.env\" ] && . \"\$HOME/.rox/$1.env\"" file
    for file in "$HOME/.profile" "$HOME/.bashrc" "$HOME/.zshenv"; do
        grep -qxF "$line" "$file" 2>/dev/null || printf '%s\n' "$line" >>"$file"
    done
}

complete_claude_onboarding() {
    # Rox supplies the credentials, so skip Claude Code's first-run sign-in screens.
    local file="$HOME/.claude.json"
    if [[ ! -s $file ]]; then
        printf '{"hasCompletedOnboarding": true}\n' >"$file"
    elif ! grep -q '"hasCompletedOnboarding"' "$file"; then
        perl -0pi -e 's/\A\s*\{\s*\}\s*\z/{"hasCompletedOnboarding": true}\n/ or s/\A\s*\{/{"hasCompletedOnboarding": true,/' "$file"
    fi
}

installed_version() {
    command -v "$1" >/dev/null || return 0
    "$1" --version 2>/dev/null | grep -Eo '[0-9]+\.[0-9]+\.[0-9]+[^ ]*' | head -n 1 || true
}
case "$stage" in
    prepare)
        command -v rex >/dev/null || { echo 'Install Rex in the guest and put it on PATH.' >&2; exit 1; }
        command -v git >/dev/null || { echo 'Install Git in the guest.' >&2; exit 1; }
        base="$HOME/.rox/$1"
        mkdir -p -m 700 "$base"
        # Projects are checked out in $HOME, by project id. The launcher picks the path.
        printf '%s\n' "$base" "$base/r.sock"
        ;;
    bake-prepare)
        command -v git >/dev/null || { echo 'Install Git in the guest.' >&2; exit 1; }
        # $1 is the project's id. Its checkout goes where a guest from the image keeps it, and the Git
        # server pins go beside rox's other state.
        rm -rf "$HOME/.rox/image" "${HOME:?}/$1"
        mkdir -p -m 700 "$HOME/.rox/image"
        printf '%s\n' "$HOME/$1" "$HOME/.rox/image/git_known_hosts"
        ;;
    clone)
        workspace=$1
        url=$2
        branch=$3
        known_hosts=$5
        if [[ $4 == yes ]]; then
            if [[ ! -S ${SSH_AUTH_SOCK:-} ]]; then
                echo 'The guest SSH server did not forward the Git agent.' >&2
                exit 1
            fi
        else
            unset SSH_AUTH_SOCK
        fi

        export GIT_TERMINAL_PROMPT=0 GIT_ALLOW_PROTOCOL=ssh:https
        export GIT_SSH_COMMAND='ssh -F /dev/null -o BatchMode=yes -o StrictHostKeyChecking=yes -o ForwardAgent=no'
        if [[ -f $known_hosts ]]; then
            printf -v known_hosts_option '%q' "UserKnownHostsFile=\"$known_hosts\""
            GIT_SSH_COMMAND+=" -o $known_hosts_option"
        fi
        if [[ -f $known_hosts ]]; then
            # Keep the pinned Git server keys, so later fetches and pushes in the guest trust them too.
            mkdir -p -m 700 "$HOME/.ssh"
            touch "$HOME/.ssh/known_hosts"
            while IFS= read -r line; do
                grep -qxF -- "$line" "$HOME/.ssh/known_hosts" || printf '%s\n' "$line" >>"$HOME/.ssh/known_hosts"
            done <"$known_hosts"
        fi
        if [[ ${6:-} == local ]]; then
            # The launcher sends its own commits and files next, so the branch needn't be on the remote yet.
            git -c credential.helper= clone --quiet --no-checkout -- "$url" "$workspace" </dev/null
            exit 0
        fi
        if [[ ! -d $workspace/.git ]]; then
            git -c credential.helper= clone --branch "$branch" -- "$url" "$workspace" </dev/null
            exit 0
        fi

        # Bring a project image's checkout up to the branch tip, keeping its prepared files.
        cd "$workspace"
        git remote set-url origin "$url"
        git -c credential.helper= fetch --quiet --no-tags origin \
            "+refs/heads/$branch:refs/remotes/origin/$branch" </dev/null
        tip=$(git rev-parse "refs/remotes/origin/$branch^{commit}")
        if [[ $(git symbolic-ref --quiet --short HEAD || true) != "$branch" || $(git rev-parse HEAD) != "$tip" ]]; then
            git checkout --quiet --force -B "$branch" "$tip"
            git branch --quiet --set-upstream-to="origin/$branch"
        fi
        printf 'Checkout is at %s (%s)\n' "$(git rev-parse --short HEAD)" "$branch" >&2
        ;;
    local)
        # Check out the launcher's HEAD, then lay its uncommitted files over it, as rox up --local sent them.
        workspace=$1
        payload=$2
        branch=$3
        head=$4
        cd "$workspace"
        if [[ -f $payload/commits.bundle ]]; then
            git fetch --quiet --no-tags "$payload/commits.bundle" HEAD </dev/null
        fi
        git checkout --quiet --force -B "$branch" "$head"
        if git rev-parse --verify --quiet "refs/remotes/origin/$branch" >/dev/null; then
            git branch --quiet --set-upstream-to="origin/$branch"
        fi
        if [[ -f $payload/deleted ]]; then
            while IFS= read -r -d '' path; do
                rm -f -- "$path"
            done <"$payload/deleted"
        fi
        if [[ -f $payload/files.tar ]]; then
            tar -xf "$payload/files.tar"
        fi
        rm -rf -- "$payload"
        printf 'Checkout is at %s (%s) with the launcher'\''s uncommitted changes\n' \
            "$(git rev-parse --short HEAD)" "$branch" >&2
        ;;
    setup)
        cd "$1"
        export ROX_PROJECT_DIR="$PWD" ROX_NAME="$2" ROX_BACKEND="$3" ROX_PHASE=boot
        export REX_SERVER="unix://$4"
        unset SSH_AUTH_SOCK
        if [[ $5 == prebuilt ]]; then
            printf 'Skipping .rox.sh in %s: it ran when the image was built\n' "$ROX_NAME" >&2
        elif [[ $5 == false && -f .rox.sh ]]; then
            printf 'Running .rox.sh in %s\n' "$ROX_NAME" >&2
            bash .rox.sh </dev/null
        fi
        ;;
    bake)
        cd "$1"
        export ROX_PROJECT_DIR="$PWD" ROX_BACKEND="$2" ROX_NAME="$3" ROX_PHASE=build
        unset REX_SERVER SSH_AUTH_SOCK
        printf 'Running .rox.sh for %s\n' "$ROX_NAME" >&2
        bash .rox.sh </dev/null
        ;;
    server)
        base="${1%/*}"
        # Rox forwards the launcher's Git keys to this path, when the guest has them.
        export SSH_AUTH_SOCK="$base/git-agent.sock"
        endpoint="unix://$1"
        # Rex doesn't tell the guest's terminals which server they're in, so rex there would look
        # for a server at the default socket. The server and every shell get this one.
        mkdir -p "$HOME/.rox"
        printf '[ -n "${REX_SERVER:-}" ] || export REX_SERVER=%q\n' "$endpoint" >"$HOME/.rox/rex.env"
        hook_env rex
        rex_ready() {
            [[ -S $1 ]] && rex --autostart=false --timeout 1s -S "$endpoint" ls >/dev/null 2>&1
        }
        if rex_ready "$1"; then
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
            if rex_ready "$1"; then
                exit 0
            fi
            kill -0 "$server_pid" 2>/dev/null || break
            sleep 0.2
        done
        echo "Rex did not answer inside the guest at $1." >&2
        cat "$log" >&2
        exit 1
        ;;
    session)
        exec "$@" </dev/null
        ;;
    agents)
        # Each argument is AGENT=VERSION, where VERSION may be latest.
        # The agents' credentials.
        hook_env agents
        for spec; do
            agent=${spec%%=*}
            version=${spec#*=}
            current=$(installed_version "$agent")
            if [[ -n $current && ($version == latest || $version == "$current") ]]; then
                printf '%s %s is installed\n' "$agent" "$current" >&2
            else
                printf 'Installing %s %s\n' "$agent" "$version" >&2
                case $agent in
                    claude) curl -fsSL https://claude.ai/install.sh | bash -s -- "$version" >&2 ;;
                    codex) curl -fsSL https://chatgpt.com/codex/install.sh |
                        CODEX_RELEASE="$version" CODEX_NON_INTERACTIVE=1 sh >&2 ;;
                    *) printf 'Unknown agent %s\n' "$agent" >&2; exit 1 ;;
                esac
                current=$(installed_version "$agent")
                if [[ -z $current ]]; then
                    printf 'Installing %s did not put it on PATH\n' "$agent" >&2
                    exit 1
                fi
            fi
            if [[ $agent == claude ]]; then
                complete_claude_onboarding
            fi
        done
        ;;
    *)
        printf 'Unknown guest stage %s\n' "$stage" >&2
        exit 1
        ;;
esac
