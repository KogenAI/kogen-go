# 28-canonical-wire-session

Goal: Canonical wire/session. Time allowance: 90 minutes including verification and commit.

Dependencies: 04,14,26.

## Owned files and deliverable

`internal/provider/wire/**`, `internal/provider/session/**`. Immutable generic prefix/schema union across independent Shapes/Builds/Shape-to-Build; variable data follows; canonical controls/input-last, owned/injected/Lite, safe persisted run-scoped affinity and distinct threads, sticky turn-state, append-only raw history/usage. Broader affinity remains measured opt-in policy.; evidence: docs/work/28-canonical-wire-session.evidence.md

## Acceptance cases

P01–04,P19,V03–05,V104–107,V117, spec P1–P3,P6,P9, D-CACHE-04–06; R(session) contributed to 68. I2 bytes; I3 CLI; I5 cross-Shape.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/28-canonical-wire-session"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'provider-19,v1.2-03-consecutive-request-byte-prefix,v1.2-04-cache-key-session-headers,v1.2-05-missing-usage,v1.2-104-provider-01,v1.2-105-provider-02,v1.2-106-provider-03,v1.2-107-provider-04,v1.2-117-provider-20' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.

Replay commands, in an isolated coherent scratch `quint/prototype` (repeat per slice):

```sh
XSPEC_SLICE=../slices/session python3 harness/xspec.py spec
XSPEC_SLICE=../slices/session python3 harness/xspec.py gen --traces 500 --steps 25 --seed 17
XSPEC_SLICE=../slices/session python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" session
XSPEC_SLICE=../slices/session python3 harness/xspec.py gen --traces 500 --steps 25 --seed 23
XSPEC_SLICE=../slices/session python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" session
XSPEC_SLICE=../slices/session python3 harness/xspec.py gen --traces 500 --steps 25 --seed 41
XSPEC_SLICE=../slices/session python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" session
```
