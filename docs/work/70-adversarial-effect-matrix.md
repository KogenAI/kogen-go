# 70-adversarial-effect-matrix

Goal: Adversarial effect matrix. Time allowance: 90 minutes including verification and commit.

Dependencies: 08,10,13,19,25,30,39,53,54,63,D3.

## Owned files and deliverable

`internal/testkit/adversarial_test.go`, `tools/safety-matrix.sh`, `docs/work/SAFETY.md`. Symlink/hardlink/FIFO publication, ignore/Git poisoning, signing hangs; crash before first/after newer snapshot, ref+archive failures, adoption after record crash, terminal pending cleanup retry, post-CAS later edits on both hosts. Fixes return to owner.; evidence: docs/work/70-adversarial-effect-matrix.evidence.md

## Acceptance cases

P26,U01–10,V06,V31,V119–123, **D-REC-01–06**, safe publication matrix. I6.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/70-adversarial-effect-matrix"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'custody-01,custody-02,custody-03,custody-04,custody-05,provider-26,v1.2-06-crash-after-base-cas,v1.2-119-custody-06,v1.2-120-custody-07,v1.2-121-custody-08,v1.2-122-custody-09,v1.2-123-custody-10,v1.2-31-provider-23-tool-result-budget' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
