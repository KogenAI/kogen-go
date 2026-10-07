# 65-intent-approve-replay evidence

Recorded 8 October 2026 in the assigned worktree on branch
`kgo/65-intent-approve-replay`.

## Revisions and resolved scope

- Worker implementation commits: `e9c07ccdc1a0241bfc7d34f6cb6bada37d9a94ea`
  and `0b64501` (final branch head at evidence time). The final commit is
  signed with the configured origin identity.
- CLI source baseline: `0d31de830f50b1f8b1b42da9048869a276f1f3b8`. This worker
  did not change the command entrypoint. `cmd/kogen/main.go` still routes to
  `app.Bootstrap`; the built CLI reports `kogen: implementation bootstrap;
  command routes are not wired`. The xspec factories in this package are not
  connected to `kogen-xspec` either.
- Draft target: read-only `kogen-spec` commit
  `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`; `CHANGES-v1.3.md` §2 records
  the exact-base baseline key and cache binding. Rust reference:
  `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected
  `xspec/intent.rs`, `xspec/approve.rs`, digest/temp helpers and the core
  approval replay modules.
- Frozen oracle: `~/cx/kgo/inputs/conformance-v1.2`, source commit
  `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`, content-manifest digest
  `8d76a7ec7fbf042ef526e959f1de6d652ec1f3ade43b33ce5587f3586372a815`.
  Runner metadata identifies `v1.2+unknown` and macOS 26.7.1 arm64. Go
  `1.27.1`, Git `2.54.0`; pinned tool paths were exported for worker commands.
- Read before implementation: `docs/work/WORKER-RULES.md`, `PLAN.md`,
  `QUEUE-source.md`, `INTERFACES.md`, package 65 notes/cases and dependency
  evidence for 18, 19, 20, 54 and 64; relevant clauses in spec §§1.7.2,
  1.7.3, 2.1.3, 2.5.1 and 2.5.2; `CHANGES-v1.3.md`; Rust replay modules.

## Implemented component boundary

`internal/xspec/intentapprove` exposes production-backed `intent` and `approve`
factories. Each owns a private temporary HOME, checkout, bare origin, state
root and setup cache. Lifecycle effects use the production Intent parser,
approval `Prepare`/`Publish`, `Remove`, journal store, rooted filesystem,
supervised Git and acceptance check adapter. Observations read actual source
bytes, Git refs/packages, journal snapshots, baseline rows and check-run
counts; they do not accept an expected observation as state.

The intent slice covers Shape, approval card/commit, Remove, Adopt, Reset,
real approval refs and real claim/journal effects. The approve slice measures
the production baseline/setup caches and acceptance checks, publishes actual
approval objects, and exercises the publisher's second source read. Injected
CAS competitors create real commits and update refs through `RefPort`; one
lost CAS is retried, and two lost CAS attempts preserve the competing dangling
ref. A late-write fixture publishes supplied replacement bytes through a
rooted filesystem before the production publisher rereads and hashes them.

Cache observations use the actual v3 baseline key. The base-tree symbol is
bound to a real marker commit/tree and checked against `BaselineKey`; the
context symbol selects an adapter-version input. A check-run is counted only
when the production baseline check adapter runs. Tests cover a same-key hit,
a changed-key red baseline and a late-write mismatch that leaves approval
state unchanged.

### Digest symbol mapping

- Intent claim symbols `abcd1234`, `bbbb2222` and `cccc3333` map to the
  computed digest's matching prefix. `ffff0000` and `deadbeef` map to a
  computed nonmatching prefix by flipping its first hex nibble. Other intent
  symbols must be actual lowercase hex prefixes or are refused.
- Approve `given`/`sha` matching is determined from their literal event-string
  prefix relation. A matching claim is translated to the prefix of the digest
  computed from the exact checkout bytes; production `Prepare` verifies that
  prefix again. Output aliases are attached only after the corresponding
  bytes/ref/package were read. `prefixOk` is never read, and no expected Obs
  field is used.
- `stableBeforeCas=false` without explicit `lateIntentBytes` or
  `lateAcceptanceBytes`, and `byBad=true` without an actual multiline `by`
  value, are refused. They are not converted into invented source or argument
  values. `feas` text is not accepted as witness proof; a witness requires a
  real commit and byte-derived diff digest.

## Commands and results

Commands used for the final package verification and the one frozen-oracle
attempt:

```sh
GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 go test -count=1 -p=2 -parallel=2 ./internal/xspec/intentapprove
GIT_CONFIG_GLOBAL=/dev/null make check
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/65-intent-approve-replay"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-02,approval-03,approval-04,approval-22,approval-23,approval-24,state-08,state-29,v1.2-02-approval-hash-intent-and-test-bytes' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The worker-shell PATH was pinned to Git `2.54.0`, Go `1.27.1`, Python
`3.14.7` and Node `24.21.0` before these commands. The frozen-suite command
above is preserved as run; its output was not retried.

| Command | Result |
|---|---|
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 go test -count=1 -p=2 -parallel=2 ./internal/xspec/intentapprove` | PASS on final worker source. Covers byte-derived claim mapping and `prefixOk` independence, required late-byte/by inputs, real publication/CAS retry, preserved ref after two losses, Remove source/ref effects, journal/claim adoption, baseline cache counts/warnings, witness refusal and late-write mismatch. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | PASS on final worker source: format, vendor fingerprints, vet, all Go tests and both CLI builds. |
| `make build` | PASS before the frozen oracle command. The final `make check` also rebuilt both binaries. |
| `git diff --check` | PASS before both worker commits. |

The exact assigned frozen-oracle command was run once. Its complete JSONL result
is preserved at
`/Users/almirsarajcic/cx/kgo/evidence/65-intent-approve-replay/results.jsonl`.
It resolved 9 selected IDs / 14 instances:

| ID | Instances | Result |
|---|---:|---|
| `state-08` | 1 | FAIL |
| `state-29` | 1 | FAIL |
| `approval-02` | 3 | FAIL |
| `approval-03` | 1 | FAIL |
| `approval-04` | 1 | FAIL |
| `approval-22` | 1 | FAIL |
| `approval-23` | 4 | FAIL |
| `approval-24` | 1 | FAIL |
| `v1.2-02-approval-hash-intent-and-test-bytes` | 1 | FAIL |
| **Total** | **14** | **0 pass / 9 fail / 0 error / 0 skip** |

Every failure stopped at the unwired CLI command path: actual stderr was
`kogen: implementation bootstrap; command routes are not wired`, with empty
stdout and exit 2. The cases expected their respective approval/remove output
and exit status. `approval-24` also reported that no provider request reached
the fake server. This run does not assess the package's production behavior;
it is not a behavior pass or a historical spec conflict. It was not retried.
The command ran before the final package commits; the CLI source entrypoint was
unchanged, and the final worker-source check/build passed separately.

The final worker component tests above are the only compatible passes claimed.
No v1.2 conformance case is counted as passing.

## Draft conflicts and deferred closure

These are exact current-model input gaps, not v1.2 oracle mismatches:

- `quint/slices/intent/spec/intent.qnt:285-287` chooses
  `prefixOk` independently from the fixed hash symbol `abcd1234`. Both values
  cannot be mapped to actual byte-derived match/mismatch results without
  trusting `prefixOk`. The adapter ignores it; the shared v1.3 model must
  replace this pair with a claim symbol that carries the input result.
- `quint/slices/approve/spec/approve.qnt:29,185-186` represents a second-read
  change only as `stableBeforeCas=false` and `newSha8`, without the replacement
  source bytes. The adapter requires real late bytes and refuses this event
  when they are absent. The `byBad` flag at lines 31 and 185 can also indicate
  an invalid empty `by` without carrying the actual invalid argument bytes;
  that event is refused unless `by` contains a real multiline value.
- `quint/slices/approve/spec/approve.qnt:190-191` allows `feas="PROVEN"` as a
  model string but supplies no witness commit/diff. Spec `02-formats.md:95,102`
  requires a Witness record and its commit at `refs/kogen/witness/<slug>`.
  The adapter refuses to fabricate proof from `feas`.
- No coherent migrated/frozen v1.3 Quint cohort is available. `R(intent)` and
  `R(approve)` spec/gen/conform were not run; seeds `17`, `23`, and `41` have
  no results or divergence counts. The private command entrypoint must first
  route these factories, and the shared model/golden/adapter cohort must be
  frozen at one revision.
- I7 complete routing remains open. Integration must connect all assigned
  slices and rerun the selected behavior cases on the integrated revision.
  The exact v1.2 failures above are retained; they do not map to a behavior
  receipt.
- Planned D-* fixtures are not v1.2 IDs; wait for the shared frozen v1.3 IDs.
  Linux, optional-runtime, custody stress/race, and live comparison gates lack
  their stated external evidence. No live provider/account calls were made.

This is component evidence only. It does not establish R(intent), R(approve),
I7, or behavior acceptance.
