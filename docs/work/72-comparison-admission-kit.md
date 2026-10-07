# 72-comparison-admission-kit

Goal: Comparison admission kit. Time allowance: 75 minutes including verification and commit.

Dependencies: I7,71,D1,D2,D3.

## Owned files and deliverable

`tools/compare-manifest.py`, `tools/cache-report.py`, `tools/compare-results.py`, `docs/work/COMPARISON.md`. Freeze tasks/graders/all role+recipe/provider/host/caps/denominator/rates; Shape/Build accounting/unknown usage and effective-request validation. Draft feasible replay: eligibility/total≥95% before launch, observed≥95% designated warm with complete telemetry; arbitrary-task raw/eligible-prefix metrics separate. Prepare only, no scored cells.; evidence: docs/work/72-comparison-admission-kit.evidence.md

## Acceptance cases

Shared v1.3 full oracle/G manifest, historical 236-case results with conflicts, P1–P13, **D-CACHE-01–06**; real-task/cost/speed/build-effort admission. I8.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/72-comparison-admission-kit"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-28-provider-13-planner-no-fallback' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
