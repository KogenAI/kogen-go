# 31 Credential vault evidence

## Revisions and environment

- Worker source commit: `01674ff` (`Implement credential vault`) on `kgo/31-credential-vault`, based on `184493fda11353b32d033773211e7419f69712ad`.
- CLI source revision during the oracle run: `184493fda11353b32d033773211e7419f69712ad`. `cmd/kogen` still routes to the bootstrap message; it does not import the vault. Provider/account adapter revision: none wired.
- Target specification: `kogen-spec` commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (`v1.3-draft`). Read `spec/04-provider.md` §4.6, `spec/02-formats.md` §2.7, `CHANGES-v1.3.md`, `docs/work/WORKER-RULES.md`, `docs/work/PLAN.md`, and `docs/work/QUEUE-source.md`.
- Rust reference: `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `provider/auth/store.rs`, `provider/auth.rs`, `provider/auth/local.rs`, `provider/auth/grok/vault.rs`, and `provider/accounts.rs`.
- Frozen oracle: `~/cx/kgo/inputs/conformance-v1.2`; runner reported `v1.2+unknown`.
- Host/toolchain: macOS 26.7.1, `darwin/arm64`, Go 1.27.1, Git 2.54.0, Python runner 3.14.7.

## Implementation and local effects

- Added a typed file-backed store for ChatGPT and Grok credentials under `.kogen/credentials`. It validates provider labels and required credential fields, writes mode 0600 files with `safefs.PublishPrivate`, and anchors reads/removals/publications under a `safefs.Root` opened on the supplied home directory. The state and credential directories must be real directories with mode 0700; symlinked or broader-permission directories are refused.
- Profile rows and the host UUID are persisted atomically as mode 0600 JSON. Host IDs are UUID v4 values and remain stable across store reopen. A malformed host JSON value is replaced atomically with a new durable ID.
- Corrupt credential JSON or incomplete records produce `ErrCredentialCorrupt`; the login read helper reports the prior record as unreadable so login can replace it. Logout removes the file without parsing it, then records signed-out profile state. Replacement is atomic; symlink leaves are replaced without touching their targets, and hardlinked leaves are refused by the rooted publisher.
- `InjectedReader.Read` opens and parses the auth file on each call, checks JWT `exp`, does not check the signature as §4.6 specifies, and performs no refresh or write. Credential `String`/`GoString` methods redact secrets. Production vault code has no subprocess import or keychain backend; no test made a macOS Keychain call.
- Unit coverage exercised private modes, replacement/temp cleanup, unsafe parent and leaf handling, hardlink refusal, corrupt credential relogin/logout, ChatGPT/Grok profile persistence, host ID reuse, injected-file rereads, malformed/expired auth, token redaction, and the no-subprocess source guard. Filesystem fixtures used temporary HOME directories only. No live account or provider request was made by unit tests.

## Commands and results

Commands used the pinned worker `PATH` from `WORKER-RULES.md`.

| Command | Result |
|---|---|
| `GOMAXPROCS=2 go test -count=1 -parallel=2 ./internal/auth/vault` | Passed. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Passed on final package source: format, vendor fingerprints, `go vet`, full tests with `-parallel=2`, and both builds. |
| `git diff --check` | Passed. |
| Assigned frozen v1.2 oracle command below | Runner exit 1: 2 cases / 2 instances, 0 pass, 2 fail, 0 error, 0 skipped, 0 unimplemented. Both failures occurred before the provider/vault behavior. The run is retained and was not retried. |

The assigned command was run once at `2026-10-07T18:44:35Z`, serialized under the `~/cx/kgo/gates.lock` mkdir lock. Its complete JSONL is `/Users/almirsarajcic/cx/kgo/evidence/31-credential-vault/results.jsonl`; fixture workdirs remain under `/Users/almirsarajcic/cx/kgo/evidence/31-credential-vault/work`.

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/31-credential-vault"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'provider-19,v1.2-32-provider-21-login-flow' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved cases and observed failures:

| Case | Instances | Result |
|---|---:|---|
| `provider-19` | 1 | Failed at step 2 (`intent approve greet`): CLI exit 2, expected 0; stderr was `kogen: implementation bootstrap; command routes are not wired`. Queue execution, injected JWT reread/expiry behavior, and the zero-request assertion were not reached. |
| `v1.2-32-provider-21-login-flow` | 1 | Failed at step 1 (`provider list`): CLI exit 2, expected 0; stdout was empty instead of the two not-signed-in provider rows, and stderr was the bootstrap message. OAuth login, profile/host persistence, use, and logout were not reached. |

No selected case is counted as a compatible pass. The component unit matrix and `make check` do not substitute for these CLI/provider observations.

## Conflicts and closure gates

- Exact historical v1.2 versus target-draft conflicts for these two selected cases: none found in the relevant clauses. §4.6 retains the expired injected JWT/no-signature-check contract and the v1.2 replacement login-flow case follows the current §2.7/§4.6/§4.10 account behavior. The oracle failures above are bootstrap/wiring failures, not spec conflicts.
- Accepted gate: **component**. I2 must wire account list/use/login/logout to the fake OAuth and file store; I3 must wire the public CLI/provider route. Then rerun the selected cases on the changed integrated revision while retaining this failed result.
- Planned D-* fixtures are not frozen v1.2 cases; do not report them as passing. Wait for shared frozen v1.3 IDs and migrated inputs before claiming draft closure. The required `R(slice)` scratch-copy replay over the coherent migrated Quint cohort (500 traces × 25 steps for seeds 17, 23, and 41) was not run because that shared cohort is unavailable.
- No Linux runtime, optional-runtime, live-provider, or live-comparison evidence was produced. No account access, Keychain call, or real credential write was used.
