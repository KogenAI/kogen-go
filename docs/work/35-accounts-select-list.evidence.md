# 35 Accounts/select/list evidence

## Revisions and authority

- Account implementation: commit `3ddeefc` (`Implement account selection store`), based on `3b1bf6610e938ba5e65b97b62fa71ceda907eff1` on `kgo/35-accounts-select-list`.
- Public CLI used by the oracle: built from the worker tree; `cmd/kogen` is unchanged from base `3b1bf66` and still returns `kogen: implementation bootstrap; command routes are not wired`. No provider/account adapter is wired. `cmd/kogen-xspec` was not invoked.
- Target specification: `kogen-spec` commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (`v1.3-draft`). Read `spec/01-cli.md` §§1.7.6, 1.8; `spec/02-formats.md` §§2.6–2.7; `spec/04-provider.md` §4.10.1; `CHANGES-v1.3.md`; and `docs/work/WORKER-RULES.md`, `PLAN.md`, `QUEUE-source.md`, `INTERFACES.md`, and the package/I2 notes.
- Rust reference: `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `crates/kogen-core/src/provider/accounts.rs`, `provider/accounts/format.rs`, `provider/chatgpt.rs`, `provider/grok.rs`, and the run-account resolver in `provider/mod.rs`.
- Frozen oracle: `~/cx/kgo/inputs/conformance-v1.2`, runner metadata `v1.2+unknown`, source commit `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. The exact selected IDs resolved to 3 cases / 4 instances.
- Host/toolchain: macOS 26.7.1 arm64, Git 2.54.0, Go 1.27.1, Python 3.14.7. Pinned worker `PATH` was exported.

## Component delivered

- Added a rooted `~/.kogen/accounts.yaml` store using `safefs`. Writes use private atomic replacement and canonical serialization; the state directory must be a real mode-0700 directory, and published account files are mode 0600. Read rejects malformed files and unsafe leaves. Atomic replacement replaces a symlink leaf without writing its target.
- Added strict-subset YAML decoding through `yamlmini`, typed ChatGPT/Grok and provider-selection rows, ASCII account-label validation, sorted canonical project rows, and write-time pruning/canonicalization of rows whose checkout directories are gone.
- Added run resolution with provider precedence (environment, project selection, machine default, ChatGPT fallback) and account precedence (environment, provider project row, committed ChatGPT `account:`, provider default, `default`). A `RunAccount` captures provider, label, credential source, and selection sources so callers can retain one decision for the run. Injected auth is reported only for ChatGPT; Grok remains owned-credential only.
- Added `Use`, which validates the label and project, refuses a missing saved credential before changing state, records provider and account selection, and returns the deterministic confirmation line. Added deterministic list/login/logout formatting and the fixed deprecated-`account:` warning text for Shape/Build callers.
- Component tests cover YAML/schema validation, labels, sorted rows and missing-directory purge, private modes, atomic symlink-leaf replacement, unsafe state refusal, saved-login checks, selection precedence, ChatGPT-only injected auth, and exact output lines. Tests used temporary directories and synthetic credentials only.

## Commands and results

| Command | Result |
|---|---|
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` | PASS on final implementation `3ddeefc`: formatting, vendor fingerprints, `go vet`, all tests including `internal/auth/accounts`, and both builds. |
| `git diff --check` | PASS. |
| Assigned frozen v1.2 command below | FAIL: 3 cases / 4 instances, 0 pass, 3 fail, 0 error, 0 skipped, 0 unimplemented. All stopped at the public CLI bootstrap before account behavior. |

The assigned command was run once at `2026-10-07T20:35:17Z`, while holding and then releasing this worker's `~/cx/kgo/gates.lock/35-accounts-select-list-provider1455` directory lock. Its complete result JSONL is `/Users/almirsarajcic/cx/kgo/evidence/35-accounts-select-list/results.jsonl`; fixture workdirs are under `/Users/almirsarajcic/cx/kgo/evidence/35-accounts-select-list/work`. The acceptance run preceded the final code commit and the later addition of `RunAccount.CredentialSource`; the public CLI did not import or wire this package in that run. The final component source is covered by the passing `make check` above.

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/35-accounts-select-list"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-24-state-14-grok-account-row,v1.2-32-provider-21-login-flow,v1.2-34-format-11-account-selection' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved results and observed blockers:

| Effective case | Instances | Result and exact blocker |
|---|---:|---|
| `v1.2-24-state-14-grok-account-row` | 2 (`empty map`, `rows`) | Both fail at step 2 (`provider list`): exit 2 instead of 0; expected `chatgpt: not signed in\ngrok: not signed in\n`, got empty stdout; stderr is `kogen: implementation bootstrap; command routes are not wired`. |
| `v1.2-32-provider-21-login-flow` | 1 | Fails at step 1 (`provider list`) with the same exit/stdout/stderr mismatch. Login-flow and account behavior are not reached. |
| `v1.2-34-format-11-account-selection` | 1 | Fails at step 1 (`provider login chatgpt`): exit 2 instead of 0; expected stdout containing `chatgpt:default signed in`; stderr is `kogen: implementation bootstrap; command routes are not wired`. |

No selected case is a compatible black-box pass. The gate failures are CLI wiring blockers, not observed account-package mismatches. Exact historical v1.2 versus v1.3-draft conflicts found in the relevant clauses: none.

During development, an initial focused test attempt failed to compile because a test compared a slice-containing struct; after that correction, one test exposed the macOS canonical `/private/var` path expectation. Both fixture issues were corrected. The focused package test then passed, and the final `make check` re-ran the package tests successfully on the committed implementation. These local test failures did not involve the oracle and are retained here rather than presented as passes.

## Effects, replay, and closure gaps

- Local effects were limited to temporary home/checkouts, synthetic vault credentials, and the runner's retained fake-provider fixture files. The provider list bootstrap failure occurred before the selected login flow or any provider request. No live account, credential, or provider was accessed.
- `D(accounts)` is a planned diagnostic fixture group, not a v1.2 case. No shared frozen v1.3 IDs or coherent migrated Quint cohort are available, so no D(accounts) pass or replay result is claimed. `R(slice)` was not run; it requires the shared migrated cohort, spec phase first, 500 traces × 25 steps for seeds 17, 23, and 41, then conformance against the same-revision private binary with full observations. No replay seeds were run.
- I2 must wire real list/use/login/logout through the fake OIDC and file store and exercise the account component through routes. I3 must close the full public CLI/provider flow and full-credential behavior, then rerun selected cases on the integrated revision while retaining this failed result.
- Linux, race-detector, optional-runtime, and live-comparison evidence was not produced. No Linux or live-provider account gate is claimed.

## Gate

Accepted gate: **component**. The account implementation and local component checks are ready for I2 wiring. The v1.2 behavior cases remain open for I2/I3; the worker commit alone does not confer behavior acceptance.
