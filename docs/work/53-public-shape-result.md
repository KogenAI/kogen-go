# 53-public-shape-result

Goal: Public Shape result. Time allowance: 75 minutes including verification and commit.

Dependencies: 22,49,50,51,52.

## Owned files and deliverable

`internal/shape/run/**`. Serialization/request errors/session+validation/output without approve/commit; accounting elapsed begins at command entry and includes setup/validation/waits/failed attempts, known tokens plus unknown-usage attempts. No output/flag addition.; evidence: docs/work/53-public-shape-result.evidence.md

## Acceptance cases

H01–25,F07–09,P24–26,V33,D-SHAPE-01–06; draft fallback/accounting replacements, raw rejected sources. I5.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/53-public-shape-result"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'format-07,format-08,format-09,provider-24,provider-26,shape-01,shape-03,shape-04,shape-05,shape-06,shape-07,shape-08,shape-09,shape-10,shape-11,shape-12,shape-13,shape-15,shape-16,shape-17,shape-18,shape-21,shape-23,shape-24,shape-25,v1.2-118-provider-25,v1.2-33-shape-json-is-unsupported' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
