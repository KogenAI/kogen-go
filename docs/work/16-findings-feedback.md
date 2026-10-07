# 16-findings-feedback

Goal: Findings/feedback. Time allowance: 75 minutes including verification and commit.

Dependencies: 00.

## Owned files and deliverable

`internal/findings/**`. GNU parser identities exclude line/message, acceptance identity includes symbol, base-red comparison; bounded feedback with full JSON/log artifact, raw tail and final gate line.; evidence: docs/work/16-findings-feedback.evidence.md

## Acceptance cases

A08–09,B04,B11–21,F10 (V136). I1 baseline card; I3 gate feedback.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/16-findings-feedback"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-08,approval-09,v1.2-136-format-10,v1.2-39-build-04,v1.2-45-build-11,v1.2-46-build-12,v1.2-47-build-13,v1.2-48-build-14,v1.2-49-build-15,v1.2-50-build-16,v1.2-51-build-17,v1.2-52-build-18,v1.2-53-build-19,v1.2-54-build-20,v1.2-55-build-21' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
