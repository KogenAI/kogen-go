#!/bin/bash
# macOS Bash 3.2 compatible; no mapfile, associative arrays, flock or wait -n.
set -u
R=${KGO_REPO:-$HOME/Areas/Kogen/kogen-go}
W=${KGO_WORKTREES:-$HOME/Areas/Kogen/kogen-go-wt}
D=${KGO_STATE:-$HOME/cx/kgo}
RUNNER=${KGO_RUNNER:-$HOME/cx/run.sh}
MAX=${MAX:-4}
MAX_FIXES=${MAX_FIXES:-2}
POLL=${POLL:-10}
DEFER_LINUX=0
case "$#" in
  0) ;;
  1) [ "$1" = --defer-linux ] || { echo 'Usage: kdispatch-go.sh [--defer-linux]' >&2; exit 2; }; DEFER_LINUX=1;;
  *) echo 'Usage: kdispatch-go.sh [--defer-linux]' >&2; exit 2;;
esac
DEFERRED_LINUX="$R/docs/work/DEFERRED-LINUX.md"
has_open_linux_deferral() {
  [ -f "$DEFERRED_LINUX" ] && grep -F '| OPEN |' "$DEFERRED_LINUX" >/dev/null
}
open_linux_deferral_for() {
  [ -f "$DEFERRED_LINUX" ] && grep -F "| $1 |" "$DEFERRED_LINUX" | grep -F '| OPEN |' >/dev/null
}
if has_open_linux_deferral; then
  [ "$DEFER_LINUX" = 1 ] || { echo 'Open Linux deferrals are recorded; restart with --defer-linux to acknowledge DEFERRED-LINUX.md.' >&2; exit 2; }
else
  [ "$DEFER_LINUX" = 0 ] || { echo '--defer-linux requires an OPEN entry in docs/work/DEFERRED-LINUX.md.' >&2; exit 2; }
fi
export GOMAXPROCS=2
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$PATH"
case "$MAX:$MAX_FIXES:$POLL" in *[!0-9:]*|:*|*::*) echo 'Invalid numeric configuration' >&2; exit 2;; esac
[ "$MAX" -gt 0 ] && [ "$POLL" -gt 0 ] || exit 2
PACKAGES=(); DEPS=(); PIDS=(); STARTS=(); LAUNCHES=(); KINDS=(); FIXES=()
while read -r pkg marker deps extra; do
  [ -n "$pkg" ] || continue
  case "$pkg" in \#*) continue;; esac
  case "$pkg" in [0-9][0-9]-*|D[123]-*|I[1-8]-*) ;; *) echo "Bad package: $pkg" >&2; exit 2;; esac
  [ "$marker" = deps: ] && [ -z "${extra:-}" ] || exit 2
  PACKAGES[${#PACKAGES[@]}]=$pkg
  DEPS[${#DEPS[@]}]=$deps
  PIDS[${#PIDS[@]}]=0; STARTS[${#STARTS[@]}]=''; LAUNCHES[${#LAUNCHES[@]}]=''; KINDS[${#KINDS[@]}]=worker; FIXES[${#FIXES[@]}]=0
done < "$R/docs/work/QUEUE.txt"
N=${#PACKAGES[@]}
[ "$N" -gt 0 ] || exit 2
index_for() {
  local j id
  for ((j=0;j<N;j++)); do
    id=${PACKAGES[$j]%%-*}
    if [ "$1" = "$id" ] || [ "$1" = "${PACKAGES[$j]}" ]; then echo "$j"; return 0; fi
  done
  return 1
}
for ((i=0;i<N;i++)); do
  [ -f "$R/docs/work/${PACKAGES[$i]}.md" ] || exit 2
  [ "${DEPS[$i]}" = none ] && continue
  for dep in ${DEPS[$i]//,/ }; do index_for "$dep" >/dev/null || { echo "Unknown dependency $dep" >&2; exit 2; }; done
done
# DRY_RUN never creates state, worktrees, locks, branches or workers.
if [ "${DRY_RUN:-0}" = 1 ]; then
  DONE=(); count=0; wave=0
  echo "DRY RUN: $N packages; MAX=$MAX; model=gpt-6-luna effort=max; Bash $BASH_VERSION"
  echo "repo=$R; worktrees=$W; state=$D"
  while [ "$count" -lt "$N" ]; do
    NEXT=(); line=''
    for ((i=0;i<N;i++)); do
      [ "${DONE[$i]:-0}" = 1 ] && continue
      ready=1
      if [ "${DEPS[$i]}" != none ]; then
        for dep in ${DEPS[$i]//,/ }; do j=$(index_for "$dep"); [ "${DONE[$j]:-0}" = 1 ] || ready=0; done
      fi
      if [ "$ready" = 1 ]; then NEXT[${#NEXT[@]}]=$i; line="$line ${PACKAGES[$i]}"; fi
    done
    [ "${#NEXT[@]}" -gt 0 ] || { echo 'Dependency cycle' >&2; exit 2; }
    printf 'wave %02d:%s\n' "$wave" "$line"
    for i in "${NEXT[@]}"; do DONE[$i]=1; count=$((count+1)); done
    wave=$((wave+1))
  done
  echo 'Launch each ready package (up to MAX): ~/cx/run.sh KGO-<pkg> <worktree> <promptfile> gpt-6-luna max'
  echo 'Integrate serially: rebase main -> GIT_CONFIG_GLOBAL=/dev/null make check -> evidence gate -> --ff-only -> remove clean worktree'
  echo 'Failure: record FAILED, preserve worktree/logs, launch fix worker, retry serialized integration; never push.'
  echo 'No workers started.'
  exit 0
fi
mkdir -p "$W" "$D/state" "$D/logs" "$D/prompts" "$D/evidence" || exit 1
identity() { /bin/ps -p "$1" -o lstart= 2>/dev/null | sed 's/^ *//'; }
alive() { [ -n "$2" ] && [ "$(identity "$1")" = "$2" ] && kill -0 "$1" 2>/dev/null; }
lock() {
  local path=$1 pid born
  if mkdir "$path" 2>/dev/null; then
    printf '%s\n%s\n' "$$" "$(identity $$)" > "$path/owner"; return 0
  fi
  if [ -f "$path/owner" ]; then
    pid=$(sed -n '1p' "$path/owner"); born=$(sed -n '2p' "$path/owner")
    if ! alive "$pid" "$born"; then
      # Rename, never remove someone else's newly acquired lock.
      if mv "$path" "$path.stale.$$" 2>/dev/null; then
        rm -rf "$path.stale.$$"
        mkdir "$path" 2>/dev/null || return 1
        printf '%s\n%s\n' "$$" "$(identity $$)" > "$path/owner"; return 0
      fi
    fi
  fi
  return 1
}
unlock() { [ "$(sed -n '1p' "$1/owner" 2>/dev/null)" = "$$" ] && rm -f "$1/owner" && rmdir "$1"; }
lock "$D/dispatcher.lock" || { echo "Dispatcher lock held: $D/dispatcher.lock" >&2; exit 1; }
trap 'unlock "$D/merge.lock" 2>/dev/null || :; unlock "$D/dispatcher.lock" 2>/dev/null || :' EXIT
trap 'echo "Dispatcher stopping; active workers and worktrees retained." >&2; exit 130' INT TERM
field() { sed -n "s/^$2=//p" "$D/state/$1.state" 2>/dev/null | head -n 1; }
record() {
  local p=$1 status=$2 sha=${3:-} gate=${4:-pending} tmp="$D/state/$1.state.$$"
  printf 'status=%s\nmerged_sha=%s\naccepted_gate=%s\npid=%s\nstart_identity=%s\nlaunch=%s\nfixes=%s\ntime=%s\n' \
    "$status" "$sha" "$gate" "${PIDS[$i]}" "${STARTS[$i]}" "${LAUNCHES[$i]}" "${FIXES[$i]}" "$(date -u +%FT%TZ)" > "$tmp"
  mv "$tmp" "$D/state/$p.state"
  printf '%s %s %s %s\n' "$(date -u +%FT%TZ)" "$status" "$p" "$sha" >> "$D/events.log"
}
clean_main() { [ "$(git -C "$R" branch --show-current)" = main ] && [ -z "$(git -C "$R" status --porcelain)" ]; }
clean_main || { echo 'Main must be checked out and clean.' >&2; exit 1; }
ready_for() {
  local dep j p sha status
  [ "${DEPS[$1]}" = none ] && return 0
  for dep in ${DEPS[$1]//,/ }; do
    j=$(index_for "$dep"); p=${PACKAGES[$j]}; status=$(field "$p" status); sha=$(field "$p" merged_sha)
    [ "$status" = MERGED ] && [ -n "$sha" ] && [ "$(field "$p" accepted_gate)" != pending ] || return 1
    git -C "$R" merge-base --is-ancestor "$sha" main || return 1
  done
}
launch() {
  local kind=$1 p=${PACKAGES[$i]} wt="$W/${PACKAGES[$i]}" branch="kgo/${PACKAGES[$i]}" name prompt
  if [ "$kind" = fix ]; then FIXES[$i]=$((FIXES[$i]+1)); fi
  name="KGO-$p"; [ "$kind" = worker ] || name="$name-fix-${FIXES[$i]}"
  LAUNCHES[$i]=$name; KINDS[$i]=$kind; prompt="$D/prompts/$name.md"
  if [ ! -d "$wt" ]; then
    if git -C "$R" show-ref --verify --quiet "refs/heads/$branch"; then
      git -C "$R" worktree add "$wt" "$branch" || return 1
    else
      git -C "$R" worktree add -b "$branch" "$wt" main || return 1
    fi
  fi
  {
    cat "$R/docs/work/$p.md" "$R/docs/work/WORKER-RULES.md"
    printf '\nYou are assigned ONLY %s on branch %s. Implement and commit your bounded package. Write docs/work/%s.evidence.md and docs/work/%s.gate.json. Gate receipt: {"package":"%s","accepted_gate":"component" or "behaviour","commands":[actual commands],"gaps":[remaining closure gates],"conflicts":[exact historical conflicts]}. Component-ready is allowed before the named integration closure; integration gates require behaviour evidence. Never merge or push.\n' "$wt" "$branch" "$p" "$p" "$p"
    if [ "$kind" = fix ]; then
      printf '\nIntegration FAILED. Fix only this worktree. Resolve any ongoing rebase, run GIT_CONFIG_GLOBAL=/dev/null make check and assigned cases, update evidence, commit with normal signing. Return so dispatcher can re-merge serially. Prior integration log:\n'
      cat "$D/logs/$p.merge.log"
    fi
  } > "$prompt"
  # run.sh ends with echo and masks Codex's status: recover its explicit exit= line.
  (
    trap - EXIT INT TERM
    "$RUNNER" "$name" "$wt" "$prompt" gpt-6-luna max
    rc=$?
    err="$HOME/cx/logs/$name.err"
    if [ -f "$err" ]; then
      last=$(tail -n 1 "$err"); case "$last" in exit=*) rc=${last#exit=};; *) rc=1;; esac
    fi
    for suffix in out err last.md; do
      [ ! -f "$HOME/cx/logs/$name.$suffix" ] || cp "$HOME/cx/logs/$name.$suffix" "$D/logs/$name.$suffix"
    done
    printf '%s\n' "$rc" > "$D/logs/$name.exit.tmp"
    mv "$D/logs/$name.exit.tmp" "$D/logs/$name.exit"
  ) > "$D/logs/$name.launch.log" 2>&1 &
  PIDS[$i]=$!; STARTS[$i]=$(identity "${PIDS[$i]}")
  record "$p" RUNNING
  echo "Started $kind $p pid=${PIDS[$i]}"
}
integrate() {
  local p=${PACKAGES[$i]} wt="$W/${PACKAGES[$i]}" branch="kgo/${PACKAGES[$i]}" before sha gate
  case "$p" in
    I8-*)
      if has_open_linux_deferral; then
        echo "I8 release gate blocked by open Linux deferrals in $DEFERRED_LINUX" >&2
        return 2
      fi
      ;;
    I[1-7]-*)
      if open_linux_deferral_for "$p" && [ "$DEFER_LINUX" != 1 ]; then
        echo "$p has an open Linux deferral; restart with --defer-linux after recording it in DEFERRED-LINUX.md" >&2
        return 2
      fi
      ;;
  esac
  lock "$D/merge.lock" || return 2
  record "$p" INTEGRATING
  before=$(git -C "$R" rev-parse main)
  (
    trap - EXIT INT TERM
    set -e
    clean_main
    [ -z "$(git -C "$wt" status --porcelain)" ]
    [ "$(git -C "$wt" rev-list --count "main..$branch")" -gt 0 ]
    git -C "$wt" rebase main
    (cd "$wt" && GIT_CONFIG_GLOBAL=/dev/null make check)
    (cd "$wt" && /Users/almirsarajcic/.local/share/mise/installs/python/3.14.7/bin/python3 tools/package-gate.py "$p")
    [ -z "$(git -C "$wt" status --porcelain)" ]
    [ "$(git -C "$R" rev-parse main)" = "$before" ]
    clean_main
    git -C "$R" merge --ff-only "$branch"
  ) > "$D/logs/$p.merge.log" 2>&1
  rc=$?
  if [ "$rc" = 0 ]; then
    sha=$(git -C "$R" rev-parse main)
    gate=$(/Users/almirsarajcic/.local/share/mise/installs/python/3.14.7/bin/python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["accepted_gate"])' "$wt/docs/work/$p.gate.json")
    record "$p" MERGED "$sha" "$gate"
    # Clean removal only; branch and evidence stay available for review.
    git -C "$R" worktree remove "$wt" >> "$D/logs/$p.merge.log" 2>&1 || echo "Cleanup pending $wt" >> "$D/events.log"
    echo "Merged $p $sha ($gate)"
  else
    record "$p" FAILED
    echo "FAILED $p; see $D/logs/$p.merge.log" >&2
  fi
  unlock "$D/merge.lock"
  return "$rc"
}
# Resume persisted workers using both PID and start identity, never pgrep counts.
for ((i=0;i<N;i++)); do
  p=${PACKAGES[$i]}; status=$(field "$p" status)
  FIXES[$i]=$(field "$p" fixes); FIXES[$i]=${FIXES[$i]:-0}
  LAUNCHES[$i]=$(field "$p" launch)
  if [ "$status" = RUNNING ]; then
    PIDS[$i]=$(field "$p" pid); STARTS[$i]=$(field "$p" start_identity)
    if ! alive "${PIDS[$i]}" "${STARTS[$i]}" && [ ! -f "$D/logs/${LAUNCHES[$i]}.exit" ]; then record "$p" FAILED; PIDS[$i]=0; fi
  elif [ "$status" = INTEGRATING ]; then
    # A crash after ff-only but before MERGED is reconciled without redoing work.
    tip=$(git -C "$R" rev-parse "kgo/$p" 2>/dev/null || :)
    if [ -n "$tip" ] && git -C "$R" merge-base --is-ancestor "$tip" main; then
      gate=$(/Users/almirsarajcic/.local/share/mise/installs/python/3.14.7/bin/python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["accepted_gate"])' "$R/docs/work/$p.gate.json")
      record "$p" MERGED "$tip" "$gate"
      git -C "$R" worktree remove "$W/$p" || :
    else record "$p" FAILED; fi
  fi
done
while :; do
  running=0; merged=0; blocked=0
  for ((i=0;i<N;i++)); do
    p=${PACKAGES[$i]}; status=$(field "$p" status)
    case "$status" in
      MERGED) merged=$((merged+1));;
      RUNNING)
        if alive "${PIDS[$i]}" "${STARTS[$i]}"; then running=$((running+1))
        elif [ -f "$D/logs/${LAUNCHES[$i]}.exit" ]; then
          wait "${PIDS[$i]}" 2>/dev/null || :; PIDS[$i]=0
          if [ "$(cat "$D/logs/${LAUNCHES[$i]}.exit")" = 0 ]; then integrate || :; else record "$p" FAILED; fi
        else PIDS[$i]=0; record "$p" FAILED; fi;;
      FAILED) [ "${FIXES[$i]}" -lt "$MAX_FIXES" ] || blocked=$((blocked+1));;
    esac
  done
  # Recount merged tasks after integrations; fix workers share MAX.
  merged=0
  for ((j=0;j<N;j++)); do
    [ "$(field "${PACKAGES[$j]}" status)" != MERGED ] || merged=$((merged+1))
  done
  for ((i=0;i<N;i++)); do
    [ "$running" -lt "$MAX" ] || break
    p=${PACKAGES[$i]}; status=$(field "$p" status)
    if [ "$status" = FAILED ] && [ "${FIXES[$i]}" -lt "$MAX_FIXES" ]; then
      launch fix && running=$((running+1)) || record "$p" FAILED
    elif [ -z "$status" ] && ready_for "$i"; then
      launch worker && running=$((running+1)) || record "$p" FAILED
    fi
  done
  [ "$merged" -lt "$N" ] || { echo "All $N packages merged; see individual accepted_gate/gaps for behaviour and release status."; exit 0; }
  if [ "$running" = 0 ]; then
    echo "No ready work. Retained failures/dependencies need coordinator action; see $D/state." >&2; exit 1
  fi
  sleep "$POLL"
done
