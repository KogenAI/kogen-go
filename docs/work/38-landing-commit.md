# 38-landing-commit

Goal: Landing commit. Time allowance: 75 minutes including verification and commit.

Dependencies: 12,13,17,36,37.

## Owned files and deliverable

`internal/landing/commit/**`. Base-relative squash of builder commits, one parent, protected approved bytes/candidate-test only, no hooks/extra trailers, origin user's effective signing configuration through supervised helpers.; evidence: docs/work/38-landing-commit.evidence.md

## Acceptance cases

B02,B30,B43,U10 (V37,V64,V67,V123). I3.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/38-landing-commit"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-123-custody-10,v1.2-37-build-02,v1.2-64-build-30,v1.2-67-build-43' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
