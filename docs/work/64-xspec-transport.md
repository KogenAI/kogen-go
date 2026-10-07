# 64-xspec-transport

Goal: xspec transport. Time allowance: 60 minutes including verification and commit.

Dependencies: 00,08,12.

## Owned files and deliverable

`internal/xspec/protocol/**`. Reset/apply JSONL loop, flush/full observations, bounded input and type/tag refusal, slice factories supplied only by integration, blank HOME and temporary origins.; evidence: docs/work/64-xspec-transport.evidence.md

## Acceptance cases

Private protocol tests and no-seam/projection refusal; contributes every G replay. I2 protocol; I7 complete routing.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
GIT_CONFIG_GLOBAL=/dev/null make check
# No black-box cases assigned to this component. Record its local acceptance evidence.
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
