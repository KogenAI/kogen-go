# 41-single-rung-controller

Goal: Single-rung controller. Time allowance: 90 minutes including verification and commit.

Dependencies: 17,19,24,25,26,27,28,30,34,35,36,37,38,39,40,62.

## Owned files and deliverable

`internal/build/single/**`. B0 validates package before provider, owner claim, run creation, plan→workspace→develop/finish→verify→commit/CAS→terminal cleanup. Invalid approval no model call. Preserve terminal candidate even on caps.; evidence: docs/work/41-single-rung-controller.evidence.md

## Acceptance cases

S10–12,B02–03,B08–09,B39–43, V37–38,V43–44,V130–131; typed end-to-end errors. I3.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/41-single-rung-controller"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'build-39,build-40,build-41,build-42,state-10,v1.2-130-state-11,v1.2-131-state-12,v1.2-37-build-02,v1.2-38-build-03,v1.2-43-build-08,v1.2-44-build-09,v1.2-67-build-43' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
