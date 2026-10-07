# 12-supervised-git-port evidence

## Revisions and environment

- Worker branch: `kgo/12-supervised-git-port`.
- Implementation commit: `e0c8617ca6e4350bd84e77e680fa4f5b2d31c7fb`.
- Target spec: v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`.
- Rust reference: `a402540b39cedc7f788472297add7ae2f8a6631a1`.
- Frozen v1.2 suite reference: `0f93bad988fb8d7a8eff4e94954d1db0a046c89d`; the supplied suite copy has no Git metadata. Its `runner.py` SHA-256 is `0135fd9095294b3701fc51ebdcc23e596b75f91cd53c10f365dead6824ed8f0e`; `fake_server.py` SHA-256 is `0c23f7b9b6c5603a39ba6fe18d64930bd3d45e443efcacc8c310177f467ce08f`.
- CLI binary fingerprints after `make build`: `bin/kogen` SHA-256 `bcd1b04942733cd656d1f4095a010eec0ca2870b25f87ff47b08131896e3594b`; `bin/kogen-xspec` SHA-256 `ea72e7494cf57ac1187eca535656ca5d8f5004a24ebff41085393014b97cc274`.
- Pinned tools: Go 1.27.1, Python 3.14.7, Git 2.54.0. Host reported by the suite: `macOS-26.7.1-arm64-arm-64bit-Mach-O`.

## Implemented component

`internal/gitio` now provides supervised workspace/origin Git operations, fixed operation policies, controlled configuration for hooks and execution helpers, stdin-fed object/message content, full SHA-1/SHA-256 object ID validation, typed errors, and ref/object operations. Tests cover configuration isolation, exact-byte object hashing, identity/signer selection, full object IDs, SHA-256 repositories, ref compare-and-swap, and hanging Git/signing helpers.

## Commands and results

Commands were run from the package worktree after exporting the pinned tool paths required by `WORKER-RULES.md`.

1. `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 go test -p=2 -parallel=2 -count=1 ./internal/gitio` — passed.
2. First `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` — failed only the hanging-process timing assertion: observed `2.014100583s` against a `2s` limit. The limit was adjusted to account for the supervised config preflight; this failed run is recorded here.
3. `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` — passed after that correction, including formatting, vendor fingerprints, vet, tests, and builds.
4. Acceptance command, run once against the frozen v1.2 copy:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/12-supervised-git-port"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-02,approval-03,approval-04,approval-05,approval-06,approval-07,approval-22,v1.2-122-custody-09,v1.2-67-build-43' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

`make build` succeeded. The runner was `kogen-conformance` `v1.2+unknown` with Git 2.54.0. Results are preserved at `/Users/almirsarajcic/cx/kgo/evidence/12-supervised-git-port/results.jsonl`; its work directories remain under `/Users/almirsarajcic/cx/kgo/evidence/12-supervised-git-port/work`.

## Acceptance resolution

The command selected 9 literal case IDs and resolved 11 instances. **0 instances passed; all 9 case records failed** because the current executable reports `kogen: implementation bootstrap; command routes are not wired` before the Git operations can be exercised. These are recorded as failures, not component passes.

| Case ID | Resolved instances | Result |
| --- | ---: | --- |
| `approval-02` | `1:6`, `2:8`, `3:64` | 3 failed at the approval route |
| `approval-03` | 1 | Failed at the approval route |
| `approval-04` | 1 | Failed at the approval route |
| `approval-05` | 1 | Failed at the approval route |
| `approval-06` | 1 | Failed at the approval route |
| `approval-07` | 1 | Failed at the approval route |
| `approval-22` | 1 | Failed at the remove route |
| `v1.2-122-custody-09` | 1 | Failed at the approval route; fake provider was not reached |
| `v1.2-67-build-43` | 1 | Failed at the approval route; fake provider was not reached |

No selected assertion was identified as a v1.2/v1.3 historical conflict; the exact conflict list is empty. The suite results do not demonstrate behavior for A02–07, A22, B43, or V122 because their command routes are not wired.

## Scope and remaining closure gates

- Component-ready only. I1 basic-object and I3 identity/custody behavior still needs a wired integration path and compatible acceptance evidence.
- Queue/approval/provider/build/CAS/status integration is unavailable while the CLI routes remain unwired; package 00 remains a foundation task.
- Candidate-tree ignore semantics, immutable-base selection, and trusted private index integration require their owning integration work.
- No coherent migrated v1.3 Quint cohort is available for R(slice); do not claim replay coverage. Planned D-* fixtures are not frozen v1.2 cases; wait for shared frozen v1.3 IDs.
- Linux parity, optional runtime, and live-comparison gates were not run. No live provider or account access was used.
- Preserve the failed v1.2 result above; any later behavior run must use the integration revision and retain its own output.
