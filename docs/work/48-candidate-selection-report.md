# 48-candidate-selection-report

Goal: Candidate selection/report. Time allowance: 75 minutes including verification and commit.

Dependencies: 13,14,36,43,44,45.

## Owned files and deliverable

`internal/build/select/**`. Passing **all approved items**→blocking count→diff→earliest rank; audit timing never alters winner. Candidate refs/diffs, best unverified output, cap snapshots; draft report demoted false/advisory empty.; evidence: docs/work/48-candidate-selection-report.evidence.md

## Acceptance cases

L13–19,L23–25 (V81–87,V91–93),D-AUD-03–05; D(orchestration) requires shared observational migration where old model demotes. I4.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/48-candidate-selection-report"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-81-ladder-13,v1.2-82-ladder-14,v1.2-83-ladder-15,v1.2-84-ladder-16,v1.2-85-ladder-17,v1.2-86-ladder-18,v1.2-87-ladder-19,v1.2-91-ladder-23,v1.2-92-ladder-24,v1.2-93-ladder-25' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
