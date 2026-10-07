# 64-xspec-transport evidence

Recorded 7 October 2026 in the assigned worktree
`/Users/almirsarajcic/Areas/Kogen/kogen-go-wt/64-xspec-transport`, branch
`kgo/64-xspec-transport`.

## Revisions and resolved scope

- Protocol implementation commit: `40bac5c0a771a804cc98618bad98b761097f46ab`.
- Host/toolchain: Darwin arm64, Git `2.54.0`, Go `1.27.1`; the required
  pinned Git/Go/Python/Node PATH was exported for worker commands.
- Draft target: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`.
- Read-only Rust reference: `kogen-rs`
  `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected the private xspec
  runner/adapter and the session/retry replay transitions.
- Frozen v1.2 oracle: `~/cx/kgo/inputs/conformance-v1.2`, source commit
  `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. Its standard profiles and
  overlay contain 236 cases / 570 instances overall. The literal assigned
  case list `docs/work/64-xspec-transport.cases` is empty: this component
  resolves to **0 case IDs / 0 instances**. No black-box behavior command was
  assigned or run; no behavior pass is claimed.
- CLI/adapter: there is no production adapter revision. At the implementation
  commit, `cmd/kogen-xspec/main.go` still calls
  `app.Bootstrap("kogen-xspec", os.Stderr)`. The required whole-repository
  check stopped before binary builds, so no xspec executable was produced.
- Contract reading before implementation: `docs/work/WORKER-RULES.md`,
  `PLAN.md`, `QUEUE-source.md`, frozen `quint/ADAPTER.md`,
  `spec/04-provider.md` §§4.9.1–4.9.2 and `CHANGES-v1.3.md` §7; Rust xspec
  `main.rs`/`xspec.rs` and core session/retry replay modules.

## Implemented boundary

`internal/xspec/protocol` now provides an injected per-slice factory registry
and a long-lived JSONL loop. It validates UTF-8, caps request lines at
16,000,000 bytes, rejects malformed/duplicate envelope fields and unknown
operations/tags, and distinguishes an absent event value from JSON `null`.
Each accepted `reset` or `apply` emits the adapter's complete JSON object and
flushes before reading the next request. Invalid observations, the reserved
`no_seam` marker, unknown factories and projection requests fail without
writing a response. The package defines no slice factories or transition
policy; integration owns those and must provide isolated temporary HOME/origin
fixtures for effectful slices.

## Commands and results

| Command | Result |
|---|---|
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 go test -count=1 -parallel=2 ./internal/xspec/protocol` | PASS on the committed protocol implementation. Tests cover reset/apply, reset state, flush-before-next-read, complete observations, CRLF and final unterminated lines, malformed/type/tag refusal, duplicate fields, unknown slice/no fallback, projection refusal, `no_seam` refusal, invalid UTF-8, oversized input, invalid observations and closer cleanup. |
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 GOFLAGS='-mod=vendor -p=2' go vet ./internal/xspec/protocol` | PASS on the committed protocol implementation. |
| `git diff --check` | PASS before commit; the committed worktree is clean. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | **FAIL**, exit 2. Tool pins, formatting, vendor fingerprint verification and `go vet ./...` completed. `go test ./...` failed in `internal/gitio`; `set -e` stopped the script before either CLI build and the final mutation guard. This result is retained here; it was not rerun to replace the failed run. |

The whole-repository check ran before the final duplicate-observation-field and
`no_seam`-key validation hardening. The committed implementation passed the
targeted protocol test and vet commands above; the failed whole-repository run
was not repeated.

Exact failures from `make check`:

- `TestRunnerSeparatesStreamsAndTimesOutHangingGit`: `captured stdout = "", want "stdout-before-hang\n"; stderr=""`.
- `TestOriginPolicyUsesGlobalIdentityAndSigningHelperNotLocalHelper`:
  `global signing helper was not run` (`global-helper-ran` was absent in the
  test's temporary directory).

The package test uses an in-memory factory and makes no provider, account,
network, production HOME or origin calls. It verifies the transport boundary,
not production slice wiring or real effect behavior.

## Replay, conflicts and closure gates

- R(slice) was not run. The shared coherent migrated v1.3 Quint cohort is not
  frozen/available. Closure requires a scratch copy, spec phase, all hand
  scenarios, then 500 traces × 25 steps for each seed `17`, `23`, and `41`,
  with reset and every full observation compared against a same-revision
  private binary. No replay seed, trace result, or divergence is claimed.
- No slice factories are routed yet. Integration must connect every G slice
  to production transitions and use isolated blank HOME/temporary origin
  fixtures. I2 protocol routing and I7 complete routing remain open.
- Planned D-* labels are not frozen v1.2 cases; shared migrated v1.3 IDs and
  model/golden/adapter cohort are unavailable. No D-* gate is claimed.
- No package-64 v1.2 case IDs were assigned, so there are no exact historical
  v1.2 conflicts attributable to this component (`conflicts: []` in the gate
  receipt). The unrelated historical ladder conflict IDs remain recorded in
  `docs/work/INPUTS.md`; this package neither ran nor counts them.
- Linux, race/custody, optional-runtime and live comparison gates were not run.

This is **component** evidence only. It does not establish I2/I7 behavior
acceptance or any G replay pass.
