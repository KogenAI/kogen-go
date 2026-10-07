# 36-verification-receipts evidence

## Revisions and scope

- Worker branch: `kgo/36-verification-receipts`.
- Implementation commit: `64fa992dbff8caf71e85d1ab3c3dca9173e59439` (`Implement verification receipts`). The package test and frozen v1.2 command used this exact source tree.
- CLI source revision: `fba340e5928b2a6c6644bb056ff76b3eb1276616` (`Bootstrap the Go implementation`); current `cmd/kogen` still exits 2 with `kogen: implementation bootstrap; command routes are not wired`.
- Production acceptance/queue adapter: none wired. `AcceptanceRunner` is an internal gate seam; it is not a public CLI integration.
- Target spec: `kogen-spec` v1.3-draft `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887`. Read `spec/03-build.md` §§3.7.1–3.7.3 and 3.8.2, relevant formats, `CHANGES-v1.3.md`, `WORKER-RULES.md`, `PLAN.md`, `QUEUE-source.md`, and `docs/work/36-verification-receipts.md`.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; reviewed `gate/verification.rs`, `gate/checks.rs`, `gate/tree.rs`, and `gate/verification_support.rs`.
- Host/toolchain: macOS 26.7.1 arm64; Go 1.27.1, Git 2.54.0, Python 3.14.7. Commands used the pinned worker PATH. No live provider or account access was used.

## Component behavior and local evidence

`internal/gate` now owns the verification pass. It applies configured fixes once, restores/guards protected files, installs the exact approved acceptance bytes, captures the post-fix candidate tree, runs checks against base and candidate, and runs the approved acceptance set. A green receipt records the base tree and exact post-fix tree. Base-relative excusing requires the matching checked base tree and delegates identity subset/equal-exit rules to `findings.IsExcused`.

Checks and acceptance runs are compared against rooted workspace snapshots. Mutations are rolled back before the gate continues, including filesystem changes that do not appear in Git's candidate tree. The receipt's protected-manifest digest includes the protected manifest, the absent acceptance source path, and the exact approved candidate-test bytes. Auditor advice is stored separately and cannot recalculate the receipt, verdict, landability, or acceptance counts.

One same-seed acceptance retry is allowed for item failures without a suite failure. When retry passes, the base is checked with the same seed; at most two items also failing on base can be excused, and those IDs affect eligibility only after create-only flake evidence is persisted. A suite failure never produces a receipt. The run records retries, base observations, tree identity, check findings, and rollback paths.

The focused package command passed:

```sh
GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/gate -count=1
```

Fixtures cover fixes-before-checks and exact post-fix receipt binding; base-relative mutating-check rollback; new finding identities; audit-advice invariance; same-seed flake evidence with the two-item cap; suite-failure rejection without retry; and evidence-publication failure keeping base-red items blocking. Fixtures use temporary local Git origins and fake process/acceptance runners. No provider request was made by these tests.

## Commands and results

1. `GOMAXPROCS=2 go test -p=2 -parallel=2 ./internal/gate -count=1` — **passed** (`ok kogen-go/internal/gate`, 24.002s).

2. `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` — **failed** in the unrelated `internal/queue/lock` test `TestConcurrentAcquireCreatesOneOwner`: `concurrent Acquire() error: queue.pid is not a safe regular owner file`. The format check, vendor fingerprint check, and `go vet ./...` completed before the repository test suite. The other listed packages passed, including `internal/gate`; `make check` stopped before its two build steps. No files outside `internal/gate/**` were changed to address this failure.

3. The required `make build` — **passed**, building `bin/kogen` and `bin/kogen-xspec` with `CGO_ENABLED=0`.

4. Required frozen v1.2 command (run once, with the full standard profiles and overlay):

   ```sh
   make build
   SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
   EVIDENCE="$HOME/cx/kgo/evidence/36-verification-receipts"
   mkdir -p "$EVIDENCE"
   PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
     "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
     --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'v1.2-136-format-10,v1.2-39-build-04,v1.2-45-build-11,v1.2-46-build-12,v1.2-47-build-13,v1.2-48-build-14,v1.2-49-build-15,v1.2-50-build-16,v1.2-51-build-17,v1.2-52-build-18,v1.2-53-build-19,v1.2-54-build-20' \
     --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
     --out "$EVIDENCE/results.jsonl"
   ```

   Runner exit was 1: **0/12 passed, 12 failed, 0 errors, 0 skipped**. Each literal ID resolved to one instance. Every instance stopped at step 2 (`kogen intent approve greet {hash8:greet}`): exit 2 instead of 0, empty stdout, and stderr `kogen: implementation bootstrap; command routes are not wired`. No scenario reached queue start or verification, so no case is counted as a gate behavior pass. The complete 13-line JSONL (metadata plus 12 results) is retained at `/Users/almirsarajcic/cx/kgo/evidence/36-verification-receipts/results.jsonl`; its workdirs remain at `/Users/almirsarajcic/cx/kgo/evidence/36-verification-receipts/work`.

The run used the shared mkdir lock `$HOME/cx/kgo/gates.lock/port1455`, then released only that lock. No oracle, goldens, or other worktrees were changed. Compatible behavior passes: **0**. The runner exposed an integration blocker, not a v1.2-v1.3 semantic conflict; no historical conflict is asserted.

## Replay, gaps, and closure

No `R(slice)` replay was run, so there are no trace seeds or divergence measurements. Local flake fixtures verify that the initial candidate, retry, and base attempt receive the same deterministic seed, but they are not shared Quint replay evidence.

The planned D-AUD-02–05 cases and `D(gate)`/draft observational test are unavailable in frozen v1.2. Wait for shared frozen v1.3 IDs; do not count planned fixtures as passes. I3 remains open: approval → queue → provider → gate → CAS → status must be wired, and these selected cases must be rerun on the integrated revision with every result retained. Linux parity, optional-runtime evidence, live comparison evidence, and the coherent migrated Quint cohort required by `R(slice)` remain external closure gates.

This receipt is **component** evidence only. The worker commit does not confer behavior acceptance.
