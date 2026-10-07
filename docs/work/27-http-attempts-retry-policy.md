# 27-http-attempts-retry-policy

Goal: HTTP attempts/retry policy. Time allowance: 90 minutes including verification and commit.

Dependencies: 14,26.

## Owned files and deliverable

`internal/provider/transport/**`, `internal/provider/retry/**`. Clock starts before refresh, first actual body byte, 90 s idle/20 min total, cancellation, retry classes/jitter/budget/no second layer, partial continuation, login waits and fallback role rules.; evidence: docs/work/27-http-attempts-retry-policy.evidence.md

## Acceptance cases

P05,P10–18,P20,H24–25,L22,L35–36, V28–30,V110–116,V125; R(stream) contributed to 68; spec P7–P8. I3; I4 mid-rung wait.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/27-http-attempts-retry-policy"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'provider-10,shape-24,shape-25,v1.2-04-cache-key-session-headers,v1.2-103-ladder-35,v1.2-110-provider-08,v1.2-111-provider-09,v1.2-112-provider-11,v1.2-113-provider-12,v1.2-114-provider-14,v1.2-115-provider-17,v1.2-116-provider-18,v1.2-117-provider-20,v1.2-125-ladder-36,v1.2-28-provider-13-planner-no-fallback,v1.2-29-provider-15-idle-stall,v1.2-30-provider-16-total-cap,v1.2-90-ladder-22' \
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
```
