#!/bin/sh
# itch-publish: a rux action. rux passes the arguments, project settings and
# secrets as environment variables (see tool.json).
set -eu
set -o pipefail  # so butler's exit status survives | tee

say() { echo "$*" | tee "$RUX_RESULT"; }

target=${RUX_SETTING_TARGET:-}
[ -n "$target" ] || { say "No itch game set: put user/game in this action's 'target' setting (project settings in the rux app)."; exit 1; }

butler=$(command -v butler || true)
for b in "$HOME/bin/butler" "$HOME/Library/Application Support/itch/apps/butler/butler"; do
  [ -z "$butler" ] && [ -x "$b" ] && butler=$b
done
[ -n "$butler" ] || { say "butler isn't installed on the Mac. Install it from https://itchio.itch.io/butler (on PATH or in ~/bin)."; exit 1; }

base=${RUX_SETTING_CHANNEL:-mac}
main=${RUX_SETTING_MAIN_BRANCH:-main}
branch=${RUX_GIT_BRANCH:-}
if [ -n "${RUX_ARG_CHANNEL:-}" ]; then
  channel=$RUX_ARG_CHANNEL
elif [ -z "$branch" ] || [ "$branch" = "$main" ] || [ "$branch" = HEAD ]; then
  channel=$base
else
  channel="$base-$(printf %s "$branch" | tr -c 'A-Za-z0-9-' '-' | tr -s '-' | sed 's/^-//; s/-$//' | cut -c1-60)"
fi
case $channel in *[!A-Za-z0-9._-]*|'') say "bad channel name: $channel"; exit 1 ;; esac
version=${RUX_ARG_VERSION:-${RUX_GIT_COMMIT:-}}

if [ -n "${RUX_ARG_SOURCE:-}" ]; then
  # Build on the Mac, sandboxed: the build scripts came from the guest.
  cmd=${RUX_SETTING_BUILD_COMMAND:-make dist}
  out=${RUX_SETTING_BUILD_OUTPUT:-build/itch}
  echo "building: $cmd"
  VERSION=$version "$RUX_BIN" sandbox "$RUX_ARG_SOURCE" -- /bin/sh -c "$cmd" 2>&1 \
    || { say "the build failed; see the output above"; exit 1; }
  build=$RUX_ARG_SOURCE/$out
  [ -e "$build" ] || { say "the build command finished but didn't make $out"; exit 1; }
elif [ -n "${RUX_ARG_BUILD:-}" ]; then
  build=$RUX_ARG_BUILD
  if [ -f "$build" ] && [ "${build%.zip}" = "$build" ]; then
    mkdir -p "$RUX_WORK/wrap"   # a single executable: push it in a folder
    cp -p "$build" "$RUX_WORK/wrap/"
    chmod +x "$RUX_WORK/wrap/$(basename "$build")"
    build=$RUX_WORK/wrap
  fi
else
  say "pass --source . (build it on the Mac) or --build PATH (a finished build)"
  exit 1
fi

# --hidden only works when creating a channel, so remember which exist.
known="$RUX_STATE/channels-$(printf %s "$target" | tr '/' '_')"
touch "$known"
hidden=""
if [ "${RUX_SETTING_HIDE_NEW_CHANNELS:-no}" = yes ] && [ "$channel" != "$base" ] && ! grep -qxF "$channel" "$known"; then
  hidden=1
fi
push() {
  set -- push "$build" "$target:$channel" --if-changed
  [ -n "$version" ] && set -- "$@" --userversion "$version"
  [ -n "$hidden" ] && set -- "$@" --hidden
  echo "butler $*"
  "$butler" "$@" 2>&1 | tee "$RUX_WORK/butler.log"
}
if ! push; then
  if [ -n "$hidden" ] && grep -qi hidden "$RUX_WORK/butler.log"; then
    echo "the channel already exists; pushing again without --hidden"
    hidden=""
    push || { say "butler push failed; see the output above"; exit 1; }
  else
    say "butler push failed; see the output above"
    exit 1
  fi
fi
grep -qxF "$channel" "$known" || echo "$channel" >> "$known"

user=${target%%/*}; game=${target#*/}
{
  echo "Pushed to $target:$channel${version:+ (version $version)}."
  [ -n "$hidden" ] && echo "New channel, created hidden: only you see it, on the game's edit page on itch.io."
  echo "Game page: https://$user.itch.io/$game"
  echo "itch takes a minute or two to process a build before the itch app offers it."
} > "$RUX_RESULT"
