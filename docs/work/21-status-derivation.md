# 21-status-derivation

Goal: Status derivation. Time allowance: 75 minutes including verification and commit.

Dependencies: 04,05,12,14.

## Owned files and deliverable

`internal/status/derive/**`. Reachable trailers, current approval/run precedence, claim owner, interrupted, dependency invalid/unknown/cycle blocking, priority/time/slug order, reused-slug semantics per frozen delta.; evidence: docs/work/21-status-derivation.evidence.md

## Acceptance cases

S17–24,S30,C25–26, V22–26,V132; R(status) contributed to 66. I1 synthetic states; I3 live.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/21-status-derivation"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'state-17,state-18,state-19,state-21,state-24,state-30,v1.2-132-state-23,v1.2-22-cli-25-status-overview,v1.2-23-cli-26-status-slug,v1.2-24-state-14-grok-account-row,v1.2-25-state-20-status-next,v1.2-26-state-22-status-next' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.

Replay commands, in an isolated coherent scratch `quint/prototype` (repeat per slice):

```sh
XSPEC_SLICE=../slices/status python3 harness/xspec.py spec
XSPEC_SLICE=../slices/status python3 harness/xspec.py gen --traces 500 --steps 25 --seed 17
XSPEC_SLICE=../slices/status python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" status
XSPEC_SLICE=../slices/status python3 harness/xspec.py gen --traces 500 --steps 25 --seed 23
XSPEC_SLICE=../slices/status python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" status
XSPEC_SLICE=../slices/status python3 harness/xspec.py gen --traces 500 --steps 25 --seed 41
XSPEC_SLICE=../slices/status python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" status
```
