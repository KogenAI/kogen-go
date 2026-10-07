# 58-witness-path

Goal: Witness path. Time allowance: 90 minutes including verification and commit.

Dependencies: 19,44,47,53.

## Owned files and deliverable

`internal/shape/witness/**`. Opt-in throwaway witness, red adjudication TEST-WRONG/WITNESS-WRONG/UNDECIDED, proof binding, approval refusal and zero-call reverify before Build ladder, no demotion.; evidence: docs/work/58-witness-path.evidence.md

## Acceptance cases

H26,L32–33 (V135,V100–101). I6.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/58-witness-path"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-100-ladder-32,v1.2-101-ladder-33,v1.2-135-shape-26' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
