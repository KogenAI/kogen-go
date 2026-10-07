# D1-observational-audit-regression

Goal: Observational audit regression. Time allowance: 75 minutes including verification and commit.

Dependencies: 04,36,43,44,48.

## Owned files and deliverable

After 04,36,43–44,48: reserved `internal/build/audit/observational_test.go`, `internal/build/select/observational_test.go`, `docs/work/D1.md`. Production fixes return to those owners.; evidence: docs/work/D1-observational-audit-regression.evidence.md

## Acceptance cases

D-AUD-01–05: config refuses uncalibrated demotion; required A2 cannot be voted in; audit before/after selection and parallel ordering preserve count/rank/winner; malformed/duplicate warns; receipt observational/no demoted events. Old L05–11 report as conflicts, next-suite replacements green. Close I4.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/D1-observational-audit-regression"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-73-ladder-05,v1.2-74-ladder-06,v1.2-75-ladder-07,v1.2-76-ladder-08,v1.2-77-ladder-09,v1.2-78-ladder-10,v1.2-79-ladder-11' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
