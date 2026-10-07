# D3-durable-crashed-work-preservation

Goal: Durable crashed-work preservation. Time allowance: 90 minutes including verification and commit.

Dependencies: 08,10,13,14,25.

## Owned files and deliverable

After 08,10,13–14,25: `internal/recovery/preserve/**`, `docs/work/D3.md`. Production ref/archive effect implements frozen latest tree, create-only deterministic IDs/adoption, lossless private fsynced archive fallback and pending terminal retry.; evidence: docs/work/D3-durable-crashed-work-preservation.evidence.md

## Acceptance cases

D-REC-01–06: latest tracked edit/deletion/untracked nonignored/mode/symlink preserved before cleanup; prior snapshots retained; publication-record crash adoption, differing later bytes new identity; both publication failures retain workspace/sole refs and pending state; repeated recovery/live owner safe; post-CAS stays landed with later edits unverified. **Before I3.**

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
GIT_CONFIG_GLOBAL=/dev/null make check
# No black-box cases assigned to this component. Record its local acceptance evidence.
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
