# 18-approval-preparation

Goal: Approval preparation. Time allowance: 90 minutes including verification and commit.

Dependencies: 04,05,06,11,15,16,17.

## Owned files and deliverable

`internal/approval/prepare/**`. Card/identity/by/hash-first, setup→baseline→acceptance→restore. Baseline checks run on exact resolved base tree in scratch checkout if necessary; never cache dirty/stale checkout as base. Use independent v3 baseline port supplied by 54.; evidence: docs/work/18-approval-preparation.evidence.md

## Acceptance cases

A01,A03,A05–21,S09,S25,D-BASE-04; S28/D-BASE-01–05 close I5. I1 uncached base correctness.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/18-approval-preparation"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-01,approval-03,approval-05,approval-06,approval-07,approval-08,approval-09,approval-10,approval-11,approval-12,approval-13,approval-14,approval-15,approval-16,approval-17,approval-19,approval-20,approval-21,state-09,state-25,state-28,v1.2-27-approval-18-status-next' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
