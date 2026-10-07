# 42-public-drain

Goal: Public drain. Time allowance: 75 minutes including verification and commit.

Dependencies: 22,23,24,25,41.

## Owned files and deliverable

`internal/queue/drain/**`. Real public queue start/stop/detach lifecycle, signals, outcome streaming/exits/counts, attempted approval set, nonfatal subsequent Builds and queued stopped Intents. Connect via app route in I3.; evidence: docs/work/42-public-drain.evidence.md

## Acceptance cases

C27–30,B01,B31–42 (V65–66,V124,V129),U04–05. I3.

Exact command against the frozen v1.2 oracle (literal effective IDs, with overlay):

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/42-public-drain"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'build-01,build-32,build-35,build-36,build-37,build-38,build-39,build-40,build-41,build-42,cli-28,cli-29,cli-30,custody-04,custody-05,v1.2-124-build-31,v1.2-129-cli-27,v1.2-65-build-33,v1.2-66-build-34' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

## Notes

Read WORKER-RULES.md, PLAN.md, QUEUE-source.md, the relevant authoritative spec clauses and CHANGES-v1.3.md, and relevant Rust modules before implementing. Run `GIT_CONFIG_GLOBAL=/dev/null make check` plus the acceptance command. Write commands, resolved IDs/instances, revision, compatible passes, exact draft conflicts and deferred closure gates to your unique evidence note; never count unwired cases as passing. Package 00 remains a real foundation task after this bootstrap. This scaffold does not freeze interfaces or assert conformance.

R(slice) requires a scratch copy of the shared coherent migrated Quint cohort: spec, then 500 traces ×25 steps for each seed 17,23,41, and conform against the same-revision private binary; full observations only. Never change the source oracle or goldens. Planned D-* fixtures are not available v1.2 cases; record this gap and wait for shared frozen v1.3 IDs before claiming their gates. Linux, optional runtime and live comparison gates require their stated external evidence. Worker commit does not confer behaviour acceptance; compiled component evidence may be code-ready until its closure round.
