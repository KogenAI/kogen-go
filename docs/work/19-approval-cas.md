# 19-approval-cas

Goal: Approval CAS. Time allowance: 90 minutes including verification and commit.

Dependencies: 12,18.

## Owned files and deliverable

`internal/approval/publish/**`. Immutable schema-2 package, exact blobs/trailers/parent chain, late re-read digest, one lost-race retry then refusal without corrupting prior ref.; evidence: docs/work/19-approval-cas.evidence.md

## Acceptance cases

A02–04,S08–09,S29,V02; R(approve) contributed to 65. I1.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/19-approval-cas"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-02,approval-03,approval-04,state-08,state-09,state-29,v1.2-02-approval-hash-intent-and-test-bytes' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.

Replay commands, in an isolated coherent scratch `quint/prototype` (repeat per slice):

```sh
XSPEC_SLICE=../slices/approve python3 harness/xspec.py spec
XSPEC_SLICE=../slices/approve python3 harness/xspec.py gen --traces 500 --steps 25 --seed 17
XSPEC_SLICE=../slices/approve python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" approve
XSPEC_SLICE=../slices/approve python3 harness/xspec.py gen --traces 500 --steps 25 --seed 23
XSPEC_SLICE=../slices/approve python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" approve
XSPEC_SLICE=../slices/approve python3 harness/xspec.py gen --traces 500 --steps 25 --seed 41
XSPEC_SLICE=../slices/approve python3 harness/xspec.py conform -- "$KGO_BIN/kogen-xspec" approve
```
