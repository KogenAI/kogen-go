# 22-status-rendering-report

Goal: Status rendering/report. Time allowance: 75 minutes including verification and commit.

Dependencies: 21.

## Owned files and deliverable

`internal/status/render/**`, `internal/status/report/**`. Exact overview/slug/JSONL rows, watch frame change detection/separators/termination, agents, setup/gate/candidate/continuation/cache fields and <1 s target.; evidence: docs/work/22-status-rendering-report.evidence.md

## Acceptance cases

C25–28,S12,S24,S30,F12, V23,V129,V131,V137,L24–25. I1 static; I3 watch; I4 rung report.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/22-status-rendering-report"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'cli-28,state-24,state-30,v1.2-129-cli-27,v1.2-131-state-12,v1.2-137-format-12,v1.2-22-cli-25-status-overview,v1.2-23-cli-26-status-slug,v1.2-92-ladder-24,v1.2-93-ladder-25' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
