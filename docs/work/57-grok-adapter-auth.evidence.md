# 57 Grok adapter and auth evidence

## Revisions and authority

- Worker branch: `kgo/57-grok-adapter-auth`.
- Worker implementation commit: `4eb5197` (`Implement Grok adapter and device auth`), based on `8ebeaaeb34f265c76e55c29ec4a155dbc7bbff8e`.
- CLI revision: `bin/kogen` was built by the assigned command from the package worktree. The public command still returns `kogen: implementation bootstrap; command routes are not wired`; it does not import the Grok adapter.
- Adapter revision: `internal/provider/grok` and `internal/auth/grok` at worker commit `4eb5197`.
- The oracle binary was built immediately before the worker commit from the same production source; the only subsequent source change was an additional local partial-stream test. The final committed tree then passed `make check`.
- Target: `kogen-spec` commit `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (`v1.3-draft`); read `spec/03-build.md` §3.2.1, `spec/04-provider.md` §§4.1, 4.5, 4.10.2–4.10.3, `CHANGES-v1.3.md` §§4–5, and the package clauses in `PLAN.md` and `QUEUE-source.md`.
- Rust reference: `kogen-rs` commit `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `provider/auth/grok.rs`, `provider/auth/grok/oauth.rs`, `provider/auth/grok/vault.rs`, `provider/auth/grok/tests.rs`, `provider/grok.rs`, and the Grok wire/transport tests.
- Host and pins: macOS 26.7.1 arm64, Go 1.27.1, Git 2.54.0, Python 3.14.7. Commands used the pinned worker PATH and `GOMAXPROCS=2`.
- Frozen oracle: `~/cx/kgo/inputs/conformance-v1.2`, source revision `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`; runner identified itself as `v1.2+unknown`.

## Component delivered

- `internal/auth/grok` now performs xAI discovery, device-code grant and polling, waits before the first poll, handles `authorization_pending`, `slow_down`, expiry and access denial, persists the credential and Grok profile row, and refreshes saved credentials through the shared `auth/refresh` lock. A missing rotated refresh token preserves the old one. Local HTTP is accepted only for explicitly enabled loopback OAuth fixtures.
- `internal/provider/grok` builds the Grok Responses body with the shared immutable generic prefix, canonical seven-schema union, role instructions, append-only conversation history, reasoning effort and nullable usage. It sends the required proxy headers and a cryptographically random UUID v4 for every physical HTTP attempt.
- The adapter uses the shared transport, SSE assembler, conversation and retry packages. It retains raw response items and per-attempt nullable usage, continues partial streams by appending received items plus the continuation instruction, and preserves byte-identical retry bodies. A Grok 401 triggers one refresh/replay; a 403 refuses without refresh. Overload retries remain on the effective Grok model. A fresh Shape fallback conversation keeps the same effective Grok tuple, run affinity and auth manager/account.
- Local fake-endpoint tests cover device grant/poll/persistence and refresh rotation, forced-401 token recheck, rejected endpoints/discovery, provider wire and usage, overload without model switching, partial-stream continuation, fresh Shape fallback/account affinity, and provider/model refusals. All fake stores use temporary directories; no live provider or account was used.

## Commands and results

| Command | Result |
|---|---|
| `GOMAXPROCS=2 GOFLAGS='-mod=vendor -p=2' go test -count=1 -parallel=2 ./internal/auth/grok ./internal/provider/grok` | Passed on the final package source. |
| `GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null make check` | Passed on commit `4eb5197`: format, vendor fingerprints, vet, full test suite and both builds. Held and released `$HOME/cx/kgo/gates.lock/port1455`. |
| Assigned acceptance command below | `make build` passed. Runner exited 1: 4 cases / 26 instances, 0 passed, 26 failed, 0 errors, 0 skipped, 0 unimplemented. Held and released `$HOME/cx/kgo/gates.lock/port1455`. Results and workdirs are retained at `/Users/almirsarajcic/cx/kgo/evidence/57-grok-adapter-auth/results.jsonl` and `/Users/almirsarajcic/cx/kgo/evidence/57-grok-adapter-auth/work`. |

The exact assigned command was:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/57-grok-adapter-auth"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'provider-10,v1.2-01-fixed-cli-help-and-grok,v1.2-24-state-14-grok-account-row,v1.2-34-format-11-account-selection' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The runner resolved these literal cases and instances:

| Case | Instances | Result and exact boundary |
|---|---:|---|
| `v1.2-01-fixed-cli-help-and-grok` | 21 | 0 pass / 21 fail. The first instance, `1:kogen intent approve greet --conformance-unknown`, failed at step 1: expected 699 bytes of CLI error/help on stdout, got 0 bytes; stderr was `kogen: implementation bootstrap; command routes are not wired`. The Grok provider-name/logout/use instances also stopped at the same bootstrap boundary. |
| `v1.2-24-state-14-grok-account-row` | 2 | 0 pass / 2 fail. `provider list` exited 2 instead of 0 and emitted the bootstrap diagnostic rather than `chatgpt: not signed in\ngrok: not signed in\n`. |
| `provider-10` | 2 | 0 pass / 2 fail. The 401 and 403 instances stopped at `provider login chatgpt`, exit 2 instead of 0, with the bootstrap diagnostic; no provider request or refresh assertion was reached. |
| `v1.2-34-format-11-account-selection` | 1 | 0 pass / 1 fail. `provider login chatgpt` exited 2 instead of 0 and did not print the expected signed-in row; stderr was the bootstrap diagnostic. |

The failures are public CLI/adapter wiring blockers, not observed v1.2-versus-v1.3 assertion conflicts. No compatible conformance pass is claimed, and the failed oracle run was not retried. The selected cases did not reach Grok adapter behavior.

## Draft coverage and closure gates

- D-SHAPE-04–05 were exercised only by local component fixtures for fresh Grok Shape fallback, effective model/account retention, and cross-provider refusal. Those names are planned draft fixtures, not frozen v1.2 cases or shared v1.3 IDs. Wait for the shared frozen v1.3 suite before claiming those gates.
- No `R(slice)` replay was run: the coherent migrated shared v1.3 Quint cohort is unavailable. No source oracle or golden was changed. No replay seed or divergence is claimed.
- The required CLI behavior closure belongs to I5, which must wire Shape and adapter routes and rerun the selected cases on the integrated revision while retaining this failed result. This worker is component-ready only.
- Linux, optional runtime, live-provider, and live-comparison evidence were not produced. No role-manifest integration or end-to-end Shape→approval→Build acceptance is claimed.

## Exact conflicts and remaining work

- Historical conflicts in the selected acceptance assertions: none observed; execution stopped at the CLI bootstrap diagnostic before those assertions could compare behavior.
- Remaining closure: I5 behavior wiring and rerun; shared frozen D-SHAPE-04–05 IDs/fixtures; coherent shared Quint cohort for any `R(slice)` release evidence; platform/runtime and live gates when their required environments are available.
