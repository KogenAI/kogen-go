# 50-shape-conversations-accounting evidence

Recorded 8 October 2026 in the assigned worktree
`/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/50-shape-conversations-accounting`,
branch `kgo/50-shape-conversations-accounting`.

## Revisions and resolved scope

- Base worktree commit: `02378151b9c54290201a2dbcb92ff2ceb99a5008`.
- Shape-session source subtree: `6be69a840d06a0cd38b33f8f428fb04a4df57d5e`
  (staged source snapshot; the evidence/gate files were not part of this
  component snapshot).
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`;
  read `CHANGES-v1.3.md` §§4–5 and `spec/02-formats.md` §2.3,
  `spec/03-build.md` §§3.2.1–3.2.5, and provider request roles in
  `spec/04-provider.md`.
- Rust reference: `kogen-rs`
  `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected
  `crates/kogen-core/src/intent/shaping/runner/execute.rs`,
  `runner/validate.rs`, and `shaping/provider.rs`.
- CLI parser/help source last changed at
  `b19ed1c9af20fd147892090d6b20a491cc346659`. The public Shape route remains
  the bootstrap handler in `internal/app/bootstrap.go` at the base revision.
- Adapter source: canonical provider wire/session last changed at
  `f5b78d9adb137ac13983e97c2663d0553506c38d`; provider transport last changed
  at `7c42bf293f10da4364f5f246b7e350380ecc7575`. Neither was modified here.
- Frozen oracle input: `$HOME/cx/kgo/inputs/conformance-v1.2`; its runner
  reports `v1.2+unknown`. The source baseline is
  `kogen-conformance` `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`.
  The frozen input copy has no Git metadata, so the runner's version string is
  `v1.2+unknown`; the source baseline is recorded separately.
- Toolchain/host: Go `1.27.1`, Git `2.54.0`, Darwin arm64. The built
  `bin/kogen` SHA-256 is
  `579b1bff380c57349c609de2a040d82589fc9970c151e936fd3b8258c23a0710`.

The component wraps the existing `provider/session.Conversation` pointer for
each conversation. Primary passes, finish guards, tool results, responses,
style repairs and controller feedback retain the same object. Primary pass or
turn exhaustion creates one new fallback thread with the same run/cache/session
affinity, the exact original first message, and retained failure feedback. The
fallback role is checked against the effective shaper provider/model/effort;
this covers the default Sol-high tuple, an overridden ChatGPT Luna tuple and a
Grok tuple. Pass slots are 1–3 and 4–6 while receipts count only actual
validation passes. Shaper turns, HTTP retries, continuations and auditor calls
are counted separately. `PublishAccounting` writes `shape-accounting.json`
through a rooted private atomic replacement and excludes prompt, response,
credential, header-value and opaque turn-state contents.

## Commands and results

Pinned PATH was exported before each command as required. Focused test command:

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/shape/session
```

Result: PASS. The package tests cover persistent history and sticky routing,
fresh aliased fallback for default/overridden ChatGPT and Grok, pass and turn
counter resets, success at the last turn/pass, fallback exhaustion, style and
finish-guard accounting, combined coverage/audit repairs, retries versus
continuations, separate auditor usage, no fallback on provider/factory errors,
nullable usage, and safe success/failure receipt publication.

Repository check:

```sh
GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check
```

Result: PASS — formatting, vendor fingerprints, vet, all tests, and both CLI
builds passed. `make build` also passed before the frozen-suite invocation.

Frozen v1.2 command (same selected IDs and overlay as the package assignment):

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

Result: **0/10 cases and 0/11 instances passed**; 10 failed, 0 errors,
0 skipped, 0 unimplemented. Literal resolved cases were `shape-07` (1),
`shape-08` (1), `shape-09` (1), `shape-10` (1), `shape-11` (1),
`shape-12` (1), `shape-13` (1), `shape-23` (2), `shape-24` (1), and
`shape-25` (1). The runner started at `2026-10-07T21:18:06Z`, used time scale
`0.02`, and recorded Git `2.54.0` on `macOS-26.7.1-arm64-arm-64bit-Mach-O`.

All selected commands stopped at `kogen intent shape` before provider behavior:
the observed exit was 2 and stderr was exactly
`kogen: implementation bootstrap; command routes are not wired`. Each result
reported that no provider request reached the fake server. The complete failed
result is retained at
`/Users/almirsarajcic/cx/kgo/evidence/50-shape-conversations-accounting/results.jsonl`
(SHA-256
`775e24780357a479c9e1f65936fe18530b1266f1c898ee52d2886bb4a2b96fdc`).
No selected case is counted as a compatible pass. No exact v1.2/draft semantic
conflict was observed; the command route failure prevented those assertions
from running.

## Effects, conflicts and closure gates

- No live provider/account calls were made. `make build` replaced only the
  local ignored binaries; the conformance runner wrote its assigned worktree
  and retained the JSONL result above. The frozen suite, goldens and spec were
  not changed.
- Conflicts recorded: none observed. The bootstrap route failure is an
  integration gap, not a semantic v1.2/draft conflict.
- `D-SHAPE-01`–`D-SHAPE-06` are planned fixtures, not frozen v1.2 cases. Their
  release claims wait for literal IDs in a shared frozen v1.3 suite.
- This is **component-ready only**, not behavior-accepted. The public Shape
  command is not wired to this package; I5 must connect session creation,
  provider turns, validation/audits, failure/success receipt publication and
  the public output, then retain a new behavior run on the integrated revision.
- No coherent migrated v1.3 Quint cohort/private-adapter replay was available,
  so the Shape slice replay was not run. The shared package-00 freeze remains a
  foundation gate. Linux, optional-runtime and live-comparison evidence also
  remains external and was not run on this Darwin arm64 worker.

No replay failure was retried or erased. Dispatcher integration and the I5
behavior closure remain outstanding.
