# 17-protection-manifest evidence

## Revisions and environment

- Worker branch: `kgo/17-protection-manifest`.
- Worker base revision: `866e8203940bba4bea9d034234c3c9ca52e03dc8`.
- Implementation revision: `7cb7a7a` (`Implement protection manifest and restore`); the evidence and gate receipt are committed in the following package commit.
- Target spec: v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`.
- Rust reference: `a402540b39cedc7f788472297add7ae2f8a6631a`.
- Frozen v1.2 suite: `0f93bad988fb8d7a8eff4e94954d1db0a046c89d` per the planning ledger; supplied suite copy reports `kogen-conformance v1.2+unknown` and has no Git metadata.
- Host and tools: `macOS-26.7.1-arm64-arm-64bit-Mach-O`, Git 2.54.0, Go 1.27.1 darwin/arm64, Python 3.14.7.
- The `bin/kogen` and `bin/kogen-xspec` binaries were built from the exact implementation source tree at `7cb7a7a`; SHA-256 values were `cd04b6f67a4d2da0affa48597b18dc40428df90bac20843dd73a2b4890365abe` and `8d4b476d0502cca84de63230da5b67a3684e1ae6d669891429cda80e22d7eeac` respectively.
- No protection CLI adapter or application route is wired in this scaffold. The CLI revision is the same Go worktree; the acceptance adapter revision is unavailable because the protected approval/build routes do not reach this package.

## Component implementation

`internal/protection` now builds a manifest from an immutable base commit through the supervised `GitPort`. It reads the base tree with `ls-tree -rz`, then retrieves selected blobs with `cat-file --batch`. The checkout is inspected through `safefs`; workspace `HEAD`, index, Git excludes, and Git ignore rules do not select protected paths.

The glob matcher implements `**/`, `**`, `*`, `?`, character classes, nested brace alternatives, dotfile matching, and trailing-slash subtree patterns. A literal with no matching base or checkout file remains an absent-sentinel entry. Protected glob selection uses the union of base paths and checkout files, while inferred gate programs are selected only when tracked in the base or present in the checkout. Gate inference includes `.kogen/project.yaml`, configured gate paths, check/fix programs, `make` file variants, supported interpreter scripts, and `acceptance.run`; `changes_gate: true` omits those inferred/configured gate files.

The result carries exact base/approved bytes for restoration and a path-to-hash projection for approval serialization. Every non-own selected path is compared with its base bytes; a mismatch returns `CheckoutBehindBaseError` with the exact paths. Own Intent and candidate acceptance bytes are mandatory inputs. Removed acceptance sources are kept outside the manifest and are required to remain absent.

`Protector.RestoreAfterBatch` restores changed files and symlinks, removes absent paths and source acceptance copies, and uses only rooted filesystem operations. Regular files publish atomically; symlinks are staged as rooted temporary links and atomically renamed over their leaf. `Protector.Guard` reports path/expected/actual digests for pre-verification and pre-commit checks. The owning Build controller still needs to wire these methods and append restoration feedback.

Package fixtures cover spec glob forms, exact base blob selection, Make/interpreter gate inference, the absent sentinel, stale checkout paths, `.gitignore` independence, `changes_gate`, rooted restore/delete behavior, symlink-leaf replacement, and guard findings.

## Commands and results

Commands used the pinned worker PATH:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 go test -p=2 -parallel=2 -count=1 ./internal/protection
GIT_CONFIG_GLOBAL=/dev/null make check
```

Both commands passed. `make check` reported `check: format, vendor fingerprints, vet, tests and both builds passed`.

The assigned frozen-oracle command ran once and its complete JSONL is retained at `/Users/almirsarajcic/cx/kgo/evidence/17-protection-manifest/results.jsonl`; workdirs are under `/Users/almirsarajcic/cx/kgo/evidence/17-protection-manifest/work`.

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/17-protection-manifest"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-14,approval-15,approval-16,v1.2-127-build-10,v1.2-44-build-09,v1.2-55-build-21' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The runner resolved one instance per requested ID. All six failed before protection behavior was reached because `kogen intent approve` returned the scaffold diagnostic `kogen: implementation bootstrap; command routes are not wired`. Totals: 6 selected, 6 instances, 0 passed, 6 failed, 0 errors, 0 skipped. These are retained failures, not behavior passes.

| Effective ID | Resolved case | Result and first blocker |
|---|---|---|
| `approval-14` | A checkout behind its base gives exit 3 | Failed: approval exited 2 instead of 3; expected `environment/checkout_behind_base: checkout is behind main: (Makefile, checks/unit.sh\|checks/unit.sh, Makefile) differ; update your checkout first`; command routes are unwired. |
| `approval-15` | Manifest contents: protected globs, project.yaml, Makefile, the script of `sh x.sh`, an absent literal, the `acceptance.run` script | Failed at approve: expected exit 0, got 2; command routes are unwired. |
| `approval-16` | `changes_gate: true` drops the gate files | Failed at approve: expected exit 0, got 2; command routes are unwired. |
| `v1.2-127-build-10` | Fourth protected restore ends the rung | Failed at approve before provider/build; command routes are unwired. Fake provider was not reached. |
| `v1.2-44-build-09` | Shell edit to the test is restored with a note | Failed at approve before provider/build; command routes are unwired. Fake provider was not reached. |
| `v1.2-55-build-21` | Scope warning does not block landing | Failed at approve before provider/build; command routes are unwired. Fake provider was not reached. |

No historical v1.2 versus v1.3-draft assertion conflict was observed. In particular, the oracle failures above are bootstrap reachability failures, not a spec conflict. The exact conflict list is empty. No retry was made to erase the failed run.

## Effects and remaining gates

- `make build` wrote the ignored binaries under `bin/`; Go fixtures created temporary bare repositories and checkouts. The source oracle and goldens were not modified. Commands used `GIT_CONFIG_GLOBAL=/dev/null`; no global configuration, other worktree, or external account was changed or accessed.
- Component evidence is ready; behavior acceptance is open. I1 must wire approval manifest construction, hash persistence, and `checkout_behind_base` exit/diagnostic behavior. I3 must wire acceptance installation/source removal, restoration after every tool batch with the required feedback, and `Guard` before verification and commit; it must also enforce the fourth-restore rung limit. Rerun and retain the six selected cases after those routes are integrated.
- R(slice) was not run because no shared coherent migrated v1.3 Quint cohort is available. It still requires a scratch cohort copy, the spec trace, and 500 traces × 25 steps for each seed 17, 23, and 41, conformed against a same-revision private binary with full observations.
- Linux verification, optional runtime gates, live comparison, and frozen shared v1.3 D-* cases remain unavailable. Planned D-* fixtures are not v1.2 cases and are not claimed as passes.
