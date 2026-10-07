# 53 Public Shape result evidence

Recorded 8 October 2026 in the assigned worktree
`/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/53-public-shape-result`, branch
`kgo/53-public-shape-result`.

## Revisions and scope

- Worker base revision: `8779852b7e59a2d16a26a33bc4f288c5ffbb9734`. The
  component was tested from that base plus the four `internal/shape/run/**`
  source files in this change. The frozen-suite run preceded the worker commit.
- Target spec: `kogen-spec`
  `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; read `CHANGES-v1.3.md` §§4–5
  and the relevant Shape, accounting, provider-session, and output clauses.
- Worker instructions: `docs/work/WORKER-RULES.md`, `PLAN.md`,
  `QUEUE-source.md`, and `53-public-shape-result.md`.
- Rust reference: `kogen-rs`
  `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected Shape
  `runner/execute.rs`, `runner/validate.rs`, prompt construction, and provider
  session handling.
- CLI source revision: `fba340e5928b2a6c6644bb056ff76b3eb1276616`
  (`Bootstrap the Go implementation`). Public command routes still return
  `kogen: implementation bootstrap; command routes are not wired`; the run
  component is not wired to the CLI in this package.
- Existing provider session revision: `f5b78d9adb137ac13983e97c2663d0553506c38d`;
  SSE decoder revision: `657a6df295632510d4ede5eaa3f5b4c46178d1f6`;
  acceptance adapter source history: `e5e2cadf51bc2a55c3388f277ee513acf0869048`.
  The acceptance adapter and provider transport were not reached by the
  command-level oracle run.
- Host/toolchain: Go `1.27.1`, Git `2.54.0`, Darwin arm64. The conformance
  runner recorded `macOS-26.7.1-arm64-arm-64bit-Mach-O`. Built `bin/kogen`
  SHA-256: `ab7921b5dfafaf863082f747e13561a92b76125288b0617f01f82798a375005a`.
- Frozen oracle: `$HOME/cx/kgo/inputs/conformance-v1.2`, source baseline
  `kogen-conformance` `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. The frozen
  input copy has no Git metadata and reports `v1.2+unknown`.

`internal/shape/run` now coordinates the command-local Shape session, output
and error rendering, request validation, finish guards, deterministic
validation, audit traversal, and success/failure accounting publication. It
accepts a resolved role manifest and injected provider, tool, audit, validator,
and rooted filesystem ports. Passes 1–3 retain one conversation; the session
controller creates the fresh fallback conversation with the same run affinity.
Every dispatched attempt is accounted, and an attempt left open at a failed
turn is completed with unknown usage. Request observations are written to a
private transcript using an explicit allowlist of safe transport fields.
Approval and commit effects are not part of this package.

The command entrypoint must supply `StartedAt` before setup for elapsed time to
include setup, validation, waits, and failed attempts. The package publishes
accounting for failures after `Runner` construction and retains the original
request bytes for prompts and validation. No separate output or flag was
introduced.

## Local verification

Focused command:

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/shape/run
```

Result: PASS.

Repository check:

```sh
GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check
```

Result: PASS — formatting, vendor fingerprints, vet, tests, and both builds
passed. The assigned acceptance command also ran `make build`, which passed.
The focused unit test covers failure accounting and elapsed time for an empty
raw request, success output serialization, multiline error rendering, provider
follow-up wording, and unknown usage for an incomplete HTTP attempt.

## Frozen v1.2 command and result

The assigned command was run once against the frozen suite. Its resolved IDs
were exactly the IDs in `docs/work/53-public-shape-result.cases`:

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

Result: **0/27 cases and 0/57 instances passed**; 27 failed, 0 errors,
0 skipped, 0 unimplemented. Every failure stopped at the public CLI bootstrap
route, before its Shape/provider behavior could be evaluated. The observed
diagnostic was `kogen: implementation bootstrap; command routes are not
wired`; the runner reported that no provider request reached the fake server.
This is an integration gap, not a behavior pass or a semantic v1.2 conflict.

Resolved cases and instance counts:

- `shape-01`, `shape-03`, `shape-04`, `shape-06`, `shape-07`, `shape-08`,
  `shape-09`, `shape-10`, `shape-11`, `shape-12`, `shape-13`, `shape-15`,
  `shape-17`, `shape-18`, `shape-21`, `shape-24`, `shape-25`: one default
  instance each; all failed at the bootstrap route.
- `shape-05`: `1:stdin whitespace`, `2:empty file`; both failed.
- `shape-16`: `1:red`, `2:127`; both failed.
- `shape-23`: `1:shaper role`, `2:default sol high`; both failed.
- `provider-24`: `1:read file`, `2:read continue`, `3:read offset`,
  `4:read missing`, `5:read binary`, `6:read limit`, `7:search hit`,
  `8:search miss`, `9:write outside`, `10:bad arguments`; all failed.
- `provider-26`, `format-07`, `format-08`: one default instance each; all failed.
- `format-09`: `1:project_unavailable`, `2:not_a_git_repo`, `3:not_found`,
  `4:acceptance_missing`, `5:hash_mismatch`,
  `6:approval_identity_unavailable`, `7:request_unavailable not found`,
  `8:request_unavailable empty stdin`, `9:acceptance_check_failed`,
  `10:setup_failed`, `11:checkout_behind_base`,
  `12:acceptance_check_path_conflict`, `13:tool_missing`,
  `14:base_unavailable`, `15:project_config_invalid`; all failed.
- `v1.2-118-provider-25`: one default instance; failed.
- `v1.2-33-shape-json-is-unsupported`: `1:shape-02`, `2:shape-14`,
  `3:shape-19`, `4:shape-20`, `5:shape-22`; all failed.

The runner started at `2026-10-07T22:14:13Z`, used time scale `0.02`, and
recorded Git `2.54.0`. The complete failed result and work directories are
retained at
[results.jsonl](/Users/almirsarajcic/cx/kgo/evidence/53-public-shape-result/results.jsonl)
and `/Users/almirsarajcic/cx/kgo/evidence/53-public-shape-result/work/`.
Result SHA-256:
`e734638581fb8f6ed8457f259cb7ef16cf2d13f6fca96b45320192c869878d7e`.
No oracle retry was made. Test work remained under temporary directories and
the evidence work directory; the frozen suite, goldens, spec, and other
worktrees were not modified. No live provider or account calls were made.

Compatible passes: the focused component test and `make check` passed. No
conformance case is counted as compatible or passing. No historical v1.2
behavior incompatibility or v1.2/draft semantic conflict was established by
this run because the route failure preceded all relevant assertions. In
particular, `v1.2-33` failed before it could verify `--json` rejection.

## Draft fixtures, conflicts, and deferred closure

- `D-SHAPE-01`–`D-SHAPE-06` are planned fixture labels, not available frozen
  v1.2 cases. Wait for literal IDs in the shared frozen v1.3 suite before
  claiming those gates. No exact draft conflict was observed.
- I5 must wire the command route to this runner, resolved role manifest,
  provider/session transport, rooted tools and validator, then run the
  compatible Shape/format/provider cases on the integrated revision. The
  behavior receipt must retain its absolute results JSONL path.
- No coherent migrated v1.3 Shape Quint cohort was available. No R(slice)
  scratch replay was run; do not change the shared source oracle or goldens.
- Linux, optional-runtime, and live comparison gates require their stated
  external evidence. This worker ran only on Darwin arm64.

Conflicts observed: none. The oracle failure is retained as an integration
gap; it is not counted as a v1.2 incompatibility or conformance pass.
