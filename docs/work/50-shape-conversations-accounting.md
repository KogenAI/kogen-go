# 50-shape-conversations-accounting

Goal: Shape conversations/accounting. Time allowance: 90 minutes including verification and commit.

Dependencies: 27,28,29,30,49.

## Owned files and deliverable

`internal/shape/session/**`. Persistent session/sticky routing/full appended history; shape-v1.3 two conversations, ≤3 traversals/≤60 shaper turns/≤2 free style repairs each. Primary turn or pass exhaustion starts fresh effective-shaper alias once, preserving files/failure; last allowance success wins. Attempts/continuations count separately, audits separate; publish accounting receipt on failure/success.; evidence: docs/work/50-shape-conversations-accounting.evidence.md

## Acceptance cases

H07–13,H23–25 plus **D-SHAPE-01–06**; overridden/Grok fallback, no fallback on provider/environment error. I5.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/50-shape-conversations-accounting"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'shape-07,shape-08,shape-09,shape-10,shape-11,shape-12,shape-13,shape-23,shape-24,shape-25' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
