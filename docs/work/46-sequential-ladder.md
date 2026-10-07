# 46-sequential-ladder

Goal: Sequential ladder. Time allowance: 90 minutes including verification and commit.

Dependencies: 40,43,44,45,48.

## Owned files and deliverable

`internal/build/ladder/**`. Fresh workspace per rung, escalation reasons, prior-failure summaries without diffs, plan to later rungs, repeats/max_rungs/raw config, failed versus stopped, snapshots preserved.; evidence: docs/work/46-sequential-ladder.evidence.md

## Acceptance cases

L01–02,L12–13,L20–23,L26–31 (V69–70,V80–81,V88–91,V94–99),B44. I4.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/46-sequential-ladder"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-68-build-44,v1.2-69-ladder-01,v1.2-70-ladder-02,v1.2-80-ladder-12,v1.2-81-ladder-13,v1.2-88-ladder-20,v1.2-89-ladder-21,v1.2-90-ladder-22,v1.2-91-ladder-23,v1.2-94-ladder-26,v1.2-95-ladder-27,v1.2-96-ladder-28,v1.2-97-ladder-29,v1.2-98-ladder-30,v1.2-99-ladder-31' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
