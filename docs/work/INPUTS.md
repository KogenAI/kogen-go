# Frozen inputs and draft delta ledger

Recorded for package 00 on 7 October 2026. This manifest identifies the inputs
available to the Go worktree; it does not declare the CLI or any behavior
conformant.

## v1.3 draft freeze

The target is `kogen-spec` commit
`e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (2026-10-07,
“Revise v1.3 draft safety contracts and provider-aware Shape policy”). Its Git
tree is `6dee6ffde869979640da757cad203ace84c86f8e`; the SHA-256 content-manifest
digest for all 400 committed files is
`52388b2be73aade4b4fb2535cef475baefae1d3f662c1f2221afed737154468f`. The
manifest digest is SHA-256 over sorted `mode NUL path NUL SHA256(raw blob) LF`
records from that commit. Draft references in this worktree mean this commit,
not the changing `main` worktree contents.

These committed normative files were read and hashed directly from the frozen
commit:

| File | SHA-256 |
|---|---|
| `CLI-RULE.txt` | `7d47a2e26be97f5ced44eaaed4ccae8d7369805337b55a262f27d92e1f544edc` |
| `CHANGES-v1.3.md` | `848b40d20be1900494302d318f4b6b106daa37279d4796212a75ec5242b99840` |
| `spec/01-cli.md` | `591cb40cfa9fcd7c4c8763872ed8460d0f4a6a3b16706c5201f2b47aa7bc0e28` |
| `spec/02-formats.md` | `b71095b747cb43112230ce50207b143e1db2c76fa61e73d3e8d584ab22132411` |
| `spec/03-build.md` | `2e515f8e99a8469984ecdd86e4d2415725cc4de0006042778841a7e23b9203ed` |
| `spec/04-provider.md` | `23a41987deaf15d540985b81bbb6cfee29b8e05a8c2a10659fdf7e027d01e499` |
| `spec/05-sandbox-custody.md` | `f733c03366eb652d8d28d8e5f9d82b17dfc2c4e9241b851e0a3dc106df865f16` |
| `spec/06-non-goals.md` | `1a510939708825835e75cbbec24124a32242acc94bc1e24e0ae4fde8418f3e3f` |
| `quint/ADAPTER.md` | `1dcfcd51ae7a084aa1f85ae338e5454407d9207ae51b08359c7b38df22b71dc0` |
| `quint/CLASSIFICATION.md` | `7253c4d4646eac4573993253f46625d8e400946642a730a16835a77334d2b10b` |

## Toolchain and module pins

The observed worker host is `darwin/arm64`: Go `1.27.1`, Git `2.54.0`, Python
`3.14.7`, and Node `24.21.0`. `mise.toml` pins those versions and Quint
`0.33.0`. Go modules are `kogen-go` with `golang.org/x/sys v0.48.0`, vendored
and checked through `docs/work/VENDOR.sha256`; `GOTOOLCHAIN=local` prevents a
toolchain download. The module keeps `go 1.27.1`. Go 1.27.1's
`go mod tidy -diff` removes the redundant `toolchain go1.27.1` directive when
the `go` directive already equals that version, so the scaffold omits that
redundant line to keep the module graph checkable. Quint/Linux/race/live
provider verification was not run here.

## Frozen v1.2 oracle and embedded corpus

The read-only oracle input is `~/cx/kgo/inputs/conformance-v1.2`, version
`1.2`, sourced from `kogen-conformance` commit
`0f93bad988fb8d7a8eff4e94954d1db0a046c89d`. The snapshot contains 491 files
(4,230,914 bytes); the SHA-256 content-manifest digest, using sorted
`relative-path NUL byte-count:raw-bytes NUL` records, is
`8d76a7ec7fbf042ef526e959f1de6d652ec1f3ade43b33ce5587f3586372a815`.

The pinned runner resolves the standard profiles
`cli,state,approval,shape,build,ladder,provider,custody,format,v1.2` to 236
effective cases and 570 instances. The six `exunit` cases are outside that
denominator. `docs/work/00-freeze-and-scaffold.cases` assigns no black-box
cases, so package 00 resolved **0 case IDs / 0 instances** and ran no black-box
conformance command. The 236/570 count identifies the oracle only; it is not a
Go result.

The copied `internal/cli/data` corpus contains 35 files / 25,450 bytes. Its
canonical corpus SHA-256 is
`869c526d0f8e26ae482ddad1ecf47cd04ad038adbda590a5ae29481635e97d40`; hashing
uses lexical paths and the `path NUL decimal-byte-count:raw-bytes NUL` record
format. The list below binds each embedded file to the frozen snapshot.

| File | SHA-256 |
|---|---|
| `constants.json` | `3a8067044001508a2815ac801f313ba4e7b6f546c88d25a03b31553518054681` |
| `help/kogen-help.txt` | `c1bc928e632cba043052e727d7d6ff5d001c302fe3f118308f40349bb2dfd157` |
| `help/kogen-intent-approve.txt` | `d77f824d32fb21ab6966235c498d99156b18a29be637f93aacfeca88d01e3367` |
| `help/kogen-intent-remove.txt` | `a4ae9a65d1a9f81353988860f1223503f8a8ec91d562e7c654decd0933cb2651` |
| `help/kogen-intent-shape.txt` | `a42778722c278875d8fd48c9cd8d3a171971f2238b93a5216ac3179cfc3fbfc4` |
| `help/kogen-intent.txt` | `952ff50d923093868bf7df94d2f462ec512b29af83bf01d20242d972950eb404` |
| `help/kogen-provider-list.txt` | `8d2e5e31b1e443a7851438a2371b6a34007aa9a0125d4b8c82eb17082b422dc6` |
| `help/kogen-provider-login.txt` | `6dc821cf9faf6edc8e396a6953ad9d1fb4b4530e59f0267162f5e7ece81f7df7` |
| `help/kogen-provider-logout.txt` | `809f33cf6397bfb022265c7d189658c63152b414085bfa51e60bad0698e759dd` |
| `help/kogen-provider-use.txt` | `0a4a645eadf2264f016607e37efeb3d7b586691576d5231780ee220c105c1961` |
| `help/kogen-provider.txt` | `9cb34e126eb4f0fc5c4e811d910c1ad61d815c5966274ceb7356292061dd2d73` |
| `help/kogen-queue-start.txt` | `56c1732a5dd0d4730ab41b1d00dc217cfcb14d21ffe0db74994b86c4daa7dd4f` |
| `help/kogen-queue-stop.txt` | `11be1148839138bfe0cc921afd73c42c3ef607ad846415ab201e8414ab236705` |
| `help/kogen-queue.txt` | `cebe28c20c87301d3cae379f5d084e95a10c817edd720b5666f736d0d070ccb8` |
| `help/kogen-status.txt` | `a180faad45faf89c9c8f92a19fd60639d993c977d8e93ef19e8ec20ed1bfd79` |
| `help/kogen-version.txt` | `31d6dafe6cb914e20dde1cabe7d0a80e6fee1b66b197219a1ee8faa573ed3412` |
| `help/kogen.txt` | `ff294a5e6d7880d7d32d6899ef1a9a48836097ffaaa3a9d721b8fe799e78e150` |
| `lint.json` | `87f8f689e2c8abc251aa83dd64646edef3e4e35b0c504e77a4543454b8dcda0d` |
| `moved.json` | `311d689b17057e6ec124461408d2da711d587ff7f6a148ab00657487c5073310` |
| `v1.2/help/kogen-intent-approve.txt` | `d77f824d32fb21ab6966235c498d99156b18a29be637f93aacfeca88d01e3367` |
| `v1.2/help/kogen-intent-remove.txt` | `a4ae9a65d1a9f81353988860f1223503f8a8ec91d562e7c654decd0933cb2651` |
| `v1.2/help/kogen-intent-shape.txt` | `2d61825da99ebd953a74ba39f48d640704affaaa29b2312c39d7a96cbeab1f29` |
| `v1.2/help/kogen-intent.txt` | `de597987f0e63a3f1c068754662f1efe2072fed703f541992706c8996841aaee` |
| `v1.2/help/kogen-provider-list.txt` | `8d2e5e31b1e443a7851438a2371b6a34007aa9a0125d4b8c82eb17082b422dc6` |
| `v1.2/help/kogen-provider-login.txt` | `d958e441024ac084db854a64583990fba92894e9a0d07e4d896075207f275ed2` |
| `v1.2/help/kogen-provider-logout.txt` | `1fb492f4cbd3093052bc9a0ce503e2ebb7567b01d8a59dbf889d94bd7d4fbc24` |
| `v1.2/help/kogen-provider-use.txt` | `15924a12dd665162cccb9fb98d2c61d4f7f153ebc022a860569650cbbc26dc20` |
| `v1.2/help/kogen-provider.txt` | `61a2924088cd49a2437c21913cfcd540b1224f8f85863e3e132a75c9e5740236` |
| `v1.2/help/kogen-queue-start.txt` | `56c1732a5dd0d4730ab41b1d00dc217cfcb14d21ffe0db74994b86c4daa7dd4f` |
| `v1.2/help/kogen-queue-stop.txt` | `11be1148839138bfe0cc921afd73c42c3ef607ad846415ab201e8414ab236705` |
| `v1.2/help/kogen-queue.txt` | `2768657f1280471228380daea5bb9f1621c4b3e7bff07ea5079595241d264d1c` |
| `v1.2/help/kogen-status.txt` | `3c567f52f3f32d9590cc356c21878abcf8f0eb6749a6aeebbc291a88efe534b9` |
| `v1.2/help/kogen-version.txt` | `31d6dafe6cb914e20dde1cabe7d0a80e6fee1b66b197219a1ee8faa573ed3412` |
| `v1.2/help/kogen.txt` | `2d83950e020890041258878e2579f86cadbd53e477355cb6d2836982923ef012` |
| `yaml-errors.json` | `e6d8e58eae1b19a31496b0417485ad7af4428910bb5eed3c43a295327556d788` |

## Draft delta ledger and deferred closure

The shared v1.3 Quint/coherent-golden migration does not exist as a frozen
release cohort. The `D-*` names below are planning fixture labels, not v1.2
cases or frozen v1.3 IDs. Oracle owners must migrate the model, scenarios,
goldens, adapter observations and executable suite together, publish one frozen
manifest, and bind the exact spec revision before these gates can close.

| Delta | Frozen clauses / source | Planned fixture group and closure owner | Status at package 00 |
|---|---|---|---|
| **D1 — observational Build audit** | `CHANGES-v1.3.md` §1; `spec/02-formats.md` §2.3/§2.8; `spec/03-build.md` §3.0/§3.8 | `D-AUD-01–05`; packages 04, 36, 43–44, 48, D1, then I4/I7 | Not run. Auditor advice cannot change approved-item participation, verification, counts, rank, winner or landing. Receipts are observational (`demoted=false`, empty advisory list); explicit uncalibrated demotion refuses. Shared replacement IDs are unavailable. Historical ladder demotion assertions remain conflicts for I4/I8. |
| **D2 — independent baseline v3** | `CHANGES-v1.3.md` §2; `spec/02-formats.md` §2.9; `spec/03-build.md` §3.3/§3.7.2 | `D-BASE-01–05`; packages 18, 54, D2, then I5/I7 | Not run. Setup-product reuse remains separate from baseline reuse; baseline binds the exact checked base tree, setup key, checks/deadlines, relevant child environment/toolchain/platform and adapter version. Unknown identities and legacy keys miss. Shared replacement IDs are unavailable. |
| **D3 — preserve crashed work** | `CHANGES-v1.3.md` §3; `spec/02-formats.md` §2.5.3/§2.8; `spec/03-build.md` §3.10; `spec/05-sandbox-custody.md` §5.4 | `D-REC-01–06`; packages 14, 25, 37, D3, then I3/I7 | Not run. Stop writers and preserve the latest tree losslessly before workspace/ref cleanup; records are unverified; failed publication retains the workspace/sole ref and sets cleanup pending. Shared replacement IDs are unavailable. |
| **Shape accounting and fallback boundaries** | `CHANGES-v1.3.md` §4; `spec/01-cli.md` §1.7.1; `spec/03-build.md` §3.2.1/§3.2.5 | `D-SHAPE-01–06`; packages 04, 50, 52–53, then I5/I7 | Not run. Count logical turns separately from HTTP attempts, continuations and auditor turns; persist all-in accounting on success/failure; only primary turn/pass exhaustion starts one fallback; provider/environment failure does not. Shared replacement IDs are unavailable. |
| **Provider-aware Shape fallback alias** | `CHANGES-v1.3.md` §5; `spec/02-formats.md` §2.3; `spec/03-build.md` §3.2.1; `spec/04-provider.md` §4.10.1 | `D-SHAPE-04–05`; packages 04, 57, then I5/I7 | Not run. Fallback inherits the effective shaper provider/model/effort; explicit fallback role keys and cross-provider model overrides refuse. Grok stays on Grok and does not acquire an overload model switch. Shared replacement IDs are unavailable. |
| **Feasible frozen cache replay** | `CHANGES-v1.3.md` §6; `spec/04-provider.md` §4.9.5 | `D-CACHE-01–06`; packages 28, 68, 72, then I5/I7/I8 | Not run. Separate theoretical eligibility, complete telemetry and live observed threshold; impossible workloads reject before launch. Fake usage does not satisfy live release evidence. Shared replacement IDs are unavailable. |
| **Cross-session static prefix** | `CHANGES-v1.3.md` §7; `spec/04-provider.md` §4.9.1–§4.9.2 | `D-CACHE-01–06`; packages 28, 68, 72, then I5/I7/I8 | Not run. Generic instructions and complete schemas stay byte-identical across independent Shapes, Builds and Shape-to-Build; variable task/run data follows them; thread identities remain distinct. Any broader cache affinity needs measured provider/model/security separation. Shared replacement IDs are unavailable. |

The planned historical ladder diagnostic group resolves through the frozen
v1.2 overlay to these literal IDs: `v1.2-73-ladder-05`,
`v1.2-74-ladder-06`, `v1.2-75-ladder-07`, `v1.2-76-ladder-08`,
`v1.2-77-ladder-09`, `v1.2-78-ladder-10`, and `v1.2-79-ladder-11`. The
overlay manifest says 73/74/75/79 expect the v1.2 auditor to demote A2 before
repair, without v1.3's observational receipt; 76 treats `infeasible` as an
unknown non-demoting v1.2 verdict; 77/78 also migrate the old completion/tool
fixtures to v1.2 `finish` and shell-recipe requirements. These are preserved
historical diagnostic assertions, not v1.3 acceptance. Package 00 ran none of
them; I4/I8 must retain their individual results and report the demotion
assertions as v1.2/draft conflicts.

## Shared decisions still open

`CHANGES-v1.3.md` explicitly leaves independent-review findings 4, 5, 8 and
11–16 open. These are shared decisions, not local Go choices:

| Open item recorded in the draft | Affected gates |
|---|---|
| Post-CAS synchronization and durable-record reconciliation | landing, recovery, status and queue replay |
| One provider terminal policy across request, Build, Intent and drain | provider, Build, queue and status |
| Serializer and `tool_choice` rules | ChatGPT/Grok wire, session replay and Shape/Build requests |
| Shaping citations and coverage | Shape, approval and acceptance-ledger gates |
| Failure comparators | verification/gate eligibility and observational audit |
| Remaining project configuration and status schema | project loading, status rendering/reporting and CLI cases |
| Executable release freeze | all conformance/replay release claims |
| Decision provenance | comparison/admission evidence |
| Full pipeline receipts | I3 onward and release admission |

The planning review also records two unresolved cross-language tensions: recipe
text versus `experimental_r4` cases (ladder/recipe gates), and reachable
`Kogen-Intent` landing trailers versus slug reuse (landing/status/queue gates).
No package-00 default is chosen for either. A shared decision and migrated
fixtures are required before those affected gates close.

## Separate uncommitted upstream edits, excluded from this freeze

At inspection, the `kogen-spec` worktree at the frozen commit had unrelated
uncommitted Quint/harness changes. None are included in the commit or hashes
above, and none were edited by this task:

- Deleted metadata: `quint/.DS_Store`, `spec/.DS_Store`.
- Modified harness: `quint/prototype/harness/xspec.py`.
- Modified approve slice: `quint/slices/approve/golden/hand/13-by-invalid.json`, `quint/slices/approve/scenarios/13-by-invalid.json`, `quint/slices/approve/spec/approve.qnt`.
- Modified intent slice: `quint/slices/intent/golden/hand/05-remove.json`, `quint/slices/intent/scenarios/05-remove.json`, `quint/slices/intent/spec/intent.qnt`.
- Modified queue slice: `quint/slices/queue/spec/queue.qnt`.
- Modified session slice: `quint/slices/session/golden/hand/06-refusals.json`, `quint/slices/session/golden/hand/07-run-affinity.json`, `quint/slices/session/scenarios/06-refusals.json`, `quint/slices/session/scenarios/07-run-affinity.json`, `quint/slices/session/spec/session.qnt`.
- Modified stream slice: `quint/slices/stream/spec/stream.qnt`; both `golden/hand/` and `scenarios/` files for `01-switch-after-two-overloads`, `02-other-class-resets-streak`, `03-attempt-cap-and-budget`, `04-wall-bounds-the-retry`, `05-upstream-cut-continues`, `06-planner-and-fallback-model`, `07-usage-and-login-pause`, `08-pause-cap-and-shaping`, `09-incomplete-and-success`, and `10-checkpoint-and-refusals`.
- Added untracked files: `quint/prototype/harness/test_xspec.py`, `quint/slices/intent/golden/hand/07-shared-base-remove.json`, `quint/slices/intent/scenarios/07-shared-base-remove.json`, `quint/slices/stream/golden/hand/11-auth-capability-and-provider-fallback.json`, `quint/slices/stream/scenarios/11-auth-capability-and-provider-fallback.json`.

The separate `kogen-conformance` worktree is also dirty with an uncommitted
v1.3 migration (`VERSION`, `kogen_conformance/context.py` and `runner.py`,
untracked `cases/v1.3/`, `data/v1.3/`, `profiles/v1.3.json`, `quint/`,
`reference/results/v1.3/`, and `kogen_conformance/contracts.py`). It was not
used as the oracle. This package used only the frozen v1.2 input snapshot.

## Rust reference inspected

Read-only source was `kogen-rs` commit
`a402540b39cedc7f788472297add7ae2f8a6631a`. Relevant modules inspected:
`crates/kogen-core/src/provider/session.rs`,
`crates/kogen-core/src/provider/http/wire.rs`,
`crates/kogen-core/src/git.rs`,
`crates/kogen-core/src/status/model.rs`, and
`crates/kogen-test-support/src/lib.rs`. Their session, wire, effect and fixture
patterns informed the ports; their historical replay helpers were not copied.
