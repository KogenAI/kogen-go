# Deferred Linux validations

This is the append-only register for Linux checks deferred by the coordinator.
Later integration gates must append their own entries without replacing earlier
ones. An open entry is not a pass or waiver. Record the Linux host, source SHA,
exact commands, and results when closing an entry. Every entry must pass before
release gate I8 and before any public claim.

The dispatcher refuses to start while entries are open unless it is invoked with
`--defer-linux`. That option acknowledges the recorded deferral; it does not skip
checks. The I8 release gate remains blocked while any entry is open.

| Gate | Deferred Linux checks | Reason | Required closure | Status |
| --- | --- | --- | --- | --- |
| I1-public-foundation | Run the I1 integration's compatible component anchors from packages 01–22 with the v1.2 overlay on Linux, plus the integrated public CLI/project/Intent/approval/status foundation smoke. Active-Build, watch, and provider cases explicitly open for I3 remain outside this I1 selection. | No Linux runner is available on the Mac Studio. | Pass on a Linux benchmark host in the joint Go and TypeScript validation batch, before I8 and before any public claim. | OPEN |

## Linux process supervisor batch — OPEN

- Host: `kogen-bench-us` (Ubuntu, 2 vCPU), Go `1.27.1`, pinned Git `2.54.0`.
- Source SHAs: benchmark run `6133dc289fd003ae14f720058e4e36388cecde2a`; integration base `84a334ade43cddc04c6eca17d1935416be828184`; fix commit `1e031bfb87436ae341cd0d5d73f03df75b2e7a30`.
- Benchmark summary: 58 packages passed in test, 60 packages passed in race. The failures below remain open pending a passing Linux re-run.
- Exact failures:
  1. `internal/process TestBuildChildEnvironmentExactAllowlistMiseAndProjectPrecedence` (`environment_test.go:150`, `:181`): `MISE_TRUSTED_CONFIG_PATHS` was sorted while the test expected insertion order. It failed when TMPDIR sorted before `/existing` (for example `/dev/shm/...`), passed with `/tmp`, and passed on macOS with `/var/folders/...`.
  2. Stdin race: `process: write child stdin: write |1: file already closed`. In `go test -count=5 ./internal/process/`, this failed 7 times across `TestRunCombinesOutputStreamsInWriteOrder` (`run_test.go:108`), `TestRunBoundsLogAndPreservesExactTail` (`:83`), and `TestBuildChildEnvironmentUsesSupervisedMiseProbe` (`environment_test.go:216`). It was also seen in `TestRunUsesExactEnvironmentAndArguments` (`run_test.go:60`), `TestRunMapsSignalExitStatusAndMissingProgram` (`:396`), `provider/tools TestShellRunsSupervisedCommandAndMergesStreams` (`shell_test.go:88`), and `xspec/intentapprove TestIntentSliceRetainsDanglingRefAfterTwoRealCASLosses` (`slice_test.go:87`, during `supervise git init`).
  3. Race build only, 2 of 2 runs: `internal/process TestGuardianNormalCleanupStopsStrayGrandchild` (`run_test.go:344`) reported `guardian did not exit before cleanup deadline` on 2 vCPU under `-race`.
- Status: `OPEN` — awaiting Linux re-run.
