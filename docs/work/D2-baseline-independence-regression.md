# D2-baseline-independence-regression

Goal: Baseline independence regression. Time allowance: 75 minutes including verification and commit.

Dependencies: 18,54.

## Owned files and deliverable

After 18,54: reserved `internal/setupcache/baseline_v3_test.go`, `internal/approval/prepare/checked_base_test.go`, `docs/work/D2.md`. Serial handoff from owners.; evidence: docs/work/D2-baseline-independence-regression.evidence.md

## Acceptance cases

D-BASE-01–05: same-lockfile source-only tree change misses baseline but hits setup; replacement green baseline cannot excuse reintroduced defect; full-context change/unknown identity/legacy key miss; dirty checkout checked in scratch exact-base; same tree card/hash hit. v3 canonical cross-language fixture and approve baseTree trace. Close I5.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
GIT_CONFIG_GLOBAL=/dev/null make check
# No black-box cases assigned to this component. Record its local acceptance evidence.
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
