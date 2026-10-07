# 20 Removal evidence

## Revision and scope

- Branch: `kgo/20-removal`.
- Component source commit: `2b23d489cd247e85a60a0ea831755f2e5921d285`.
- Starting revision: `2d85e55d8beb63f2e387737333ac2ae5389b9cdc`.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (`v1.3-draft`). Relevant clauses: `spec/01-cli.md` §1.7.3 and `spec/02-formats.md` §2.5.3. `CHANGES-v1.3.md` has no removal-specific delta.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`, `crates/kogen-core/src/approval/remove.rs` and the path-limited commit/CAS helpers in `crates/kogen-core/src/git.rs`.
- CLI and adapter source revision used for the build: `2b23d489cd247e85a60a0ea831755f2e5921d285`. `cmd/kogen` still reaches `internal/app.Bootstrap`; `internal/xspec/intentapprove` is still a package stub. The compiled `kogen` reports `kogen: implementation bootstrap; command routes are not wired`.
- Pinned tools: Go `1.27.1 darwin/arm64`, Git `2.54.0`, Python `3.14.7`, Node `24.21.0`.
- Binary SHA-256 at the component source revision: `bin/kogen` `0e70fcb38eb20b4bd7656895634716effa5ab9ad5098196f942d78f99413b9e9`; `bin/kogen-xspec` `571c6caeceec6a5c251d7530695ce363d983d7b787d0494a984adb9fb7be9ec4`.
- Owned changes are limited to `internal/approval/remove/**` and this package's evidence and gate receipt.

## Implementation

- Removal checks the rooted Intent source, reads the origin claim and its run snapshot, and refuses a matching `running` Build before force handling. A missing or unreadable run snapshot for an existing claim fails closed.
- A landed Intent does not need `--force`. Otherwise, a present approval ref or current failed, parked, or interrupted run requires it. Approval refs with dangling object IDs count as present.
- The removal commit starts from checkout `HEAD` in a private Git index, removes only the Intent and acceptance source paths, and advances the checked-out ref with an expected-parent CAS. The real index loses only those two paths, preserving unrelated staged changes. Hooks are bypassed through `commit-tree` and supervised ref/index Git commands.
- Checkout cleanup uses the rooted filesystem API. Approval deletion uses `update-ref --no-deref -d <ref> <observed-object>` so a moved or symbolic ref cannot cause deletion of another target. A lost approval-ref race is reported as `controller/approval_ref_changed` and the competing value is preserved.
- Focused real-Git tests cover draft path-only commit and staged-index preservation, landed removal without force, approved/failed/parked/interrupted/dangling force behavior, active Build refusal with force, untracked files, and an injected owner-ref race.

## Commands and results

All commands used the pinned worker `PATH` required by `docs/work/WORKER-RULES.md`.

| Command | Result |
|---|---|
| `GOMAXPROCS=2 go test -p=2 -parallel=2 -count=1 ./internal/approval/remove` | Passed after the final source change. The tests use temporary real Git repositories and do not exercise the public command router. |
| `git diff --cached --check` | Passed for the source commit. |
| `make build` | Passed at source commit `2b23d489cd247e85a60a0ea831755f2e5921d285`; hashes are recorded above. |
| `./bin/kogen version` | Returned the bootstrap diagnostic `kogen: implementation bootstrap; command routes are not wired`; this confirms public CLI behavior is not wired, not a removal case result. |
| `GIT_CONFIG_GLOBAL=/dev/null make check` | Not run. The shared `/Users/almirsarajcic/cx/kgo/gates.lock` already existed, was empty, and had no owner marker. An atomic `mkdir` acquisition failed; this worker did not remove or reuse the lock. |
| Assigned frozen v1.2 command for `approval-22,approval-23,approval-24` | Not run because the required shared gate lock could not be acquired. No result JSONL exists; no cases or instances are counted as passing or failing. The case files describe 1 + 4 + 1 potential instances, but those were not resolved by the runner. |
| `R(intent)` | Not run. There is no wired production-backed `intent` adapter or confirmed shared coherent migrated Quint cohort. Seeds 17, 23, and 41 have no trace or divergence result. |

The required conformance command, when the shared lock is available, is:

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/20-removal"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-22,approval-23,approval-24' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

During development, one focused test run failed only because the test expected the generic approval-ref force wording for the interrupted-run branch; the implementation's `still has an approval ref` wording matches the Rust behavior, so the test expectation was corrected and the final focused suite passed. A `tools/package-gate.py --help` probe was also an invalid invocation and did not evaluate a gate; the supported package invocation is recorded in the gate receipt.

## Case outcomes and closure gates

- Historical IDs: `approval-22,approval-23,approval-24`. Runner resolution and behavior outcomes are unavailable because the runner was not launched. Compatible passes: none claimed. Exact observed v1.2 conflicts: none; no case was run. The public bootstrap would stop cases before their removal assertions, so it cannot be counted as behavior evidence.
- I1 owns public draft/force removal wiring and behavior closure. I3 owns the active-Build public case and the approval → queue → fake-provider path. Neither is closed by this component commit.
- `R(intent)` is contributed to by package 65 after the shared coherent draft cohort and same-revision production adapter are available. Do not claim Quint traces before then.
- Planned D-* fixtures are not frozen v1.2 cases. Wait for shared frozen v1.3 IDs and migrated observations. Linux native checks, optional runtimes, and live provider/account evidence were not available or attempted.
- This is component-ready evidence only; it does not confer behavior acceptance.
