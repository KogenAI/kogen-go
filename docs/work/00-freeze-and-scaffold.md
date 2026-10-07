# 00-freeze-and-scaffold

Goal: Freeze and scaffold. Time allowance: 90 minutes including verification and commit.

Dependencies: none.

## Owned files and deliverable

`go.mod`, `go.sum`, `vendor/**`, `mise.toml`, `Makefile`, `cmd/*/main.go`, `internal/contract/**`, `internal/testkit/**`, `internal/cli/data/**`, `internal/app/bootstrap.go`, `docs/work/INPUTS.md`, `docs/work/INTERFACES.md`, `tools/check.sh`. Freeze draft e19dd1c, record unrelated dirty patches separately, require coherent model/golden migration by oracle owners; embed corpus, define ports and hermetic Git fixtures.; evidence: docs/work/00-freeze-and-scaffold.evidence.md

## Acceptance cases

Compile/check/corpus hashes; delta ledger binds D1–D3 and should-fix cases. No unavailable stub is counted passing. Open serializer/provider/status ambiguities require a shared decision before affected gates; I1 infrastructure.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
GIT_CONFIG_GLOBAL=/dev/null make check
# No black-box cases assigned to this component. Record its local acceptance evidence.
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
