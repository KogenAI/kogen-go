# 43-rung-repair-machine

Goal: Rung repair machine. Time allowance: 90 minutes including verification and commit.

Dependencies: 30,36,41.

## Owned files and deliverable

`internal/build/repair/**`. Six repairs, strict decreasing red count, no-progress/no-count/unchanged ends, turn/wall cap final verification, protected restore fourth hit, exact appended controller feedback.; evidence: docs/work/43-rung-repair-machine.evidence.md

## Acceptance cases

B04–10,B44,L20–21,L27,L29–30 (V39–44,V68,V88–89,V95,V97–98,V127). I4.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/43-rung-repair-machine"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-127-build-10,v1.2-39-build-04,v1.2-40-build-05,v1.2-41-build-06,v1.2-42-build-07,v1.2-43-build-08,v1.2-44-build-09,v1.2-68-build-44,v1.2-88-ladder-20,v1.2-89-ladder-21,v1.2-95-ladder-27,v1.2-97-ladder-29,v1.2-98-ladder-30' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
