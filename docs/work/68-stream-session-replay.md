# 68-stream-session-replay

Goal: Stream/session replay. Time allowance: 75 minutes including verification and commit.

Dependencies: 27,28,34,59,64.

## Owned files and deliverable

`internal/xspec/streamsession/**`. Production retry/session port with fake clock/effect inputs, overload/capability/auth/fallback/wait handling, raw wire stability as independent assertions.; evidence: docs/work/68-stream-session-replay.evidence.md

## Acceptance cases

R(stream),R(session); retain V03–05,P10,P22, spec P1–P3,P7–P9,P13. I7.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/68-stream-session-replay"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'provider-10,provider-22,v1.2-03-consecutive-request-byte-prefix,v1.2-04-cache-key-session-headers,v1.2-05-missing-usage,v1.2-28-provider-13-planner-no-fallback' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.

Replay commands, in an isolated coherent scratch `quint/prototype` (repeat per slice):

```sh
XSPEC_SLICE=../slices/stream python3 harness/xspec.py spec
XSPEC_SLICE=../slices/stream python3 harness/xspec.py gen --traces 500 --steps 25 --seed 17
XSPEC_SLICE=../slices/stream python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" stream
XSPEC_SLICE=../slices/stream python3 harness/xspec.py gen --traces 500 --steps 25 --seed 23
XSPEC_SLICE=../slices/stream python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" stream
XSPEC_SLICE=../slices/stream python3 harness/xspec.py gen --traces 500 --steps 25 --seed 41
XSPEC_SLICE=../slices/stream python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" stream
XSPEC_SLICE=../slices/session python3 harness/xspec.py spec
XSPEC_SLICE=../slices/session python3 harness/xspec.py gen --traces 500 --steps 25 --seed 17
XSPEC_SLICE=../slices/session python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" session
XSPEC_SLICE=../slices/session python3 harness/xspec.py gen --traces 500 --steps 25 --seed 23
XSPEC_SLICE=../slices/session python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" session
XSPEC_SLICE=../slices/session python3 harness/xspec.py gen --traces 500 --steps 25 --seed 41
XSPEC_SLICE=../slices/session python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" session
```
