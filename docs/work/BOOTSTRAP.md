# Studio bootstrap record

Bootstrap only; package 00 still freezes production interfaces and the coherent draft replay cohort.

Observed host: macOS 26.7.1 / 25G241, darwin/arm64; 10 CPUs, 32 GiB RAM. Make 3.81; Bash 3.2.57. The user explicitly permits Bash 3, superseding the plan’s Bash 5 requirement. Apple clang 21.0.0 (clang-2100.1.1.101).

Pinned via repo-local mise use: Go 1.27.1, Python 3.14.7, Node 24.21.0, @informalsystems/quint 0.33.0; x/sys v0.48.0 vendored. Pinned upstream Git 2.54.0 was installed via repo-local mise; system Git remains 2.50.1 (Apple Git-155). Dispatcher/checks select upstream 2.54.0. Git was built with MAKEFLAGS=-j2, NO_GETTEXT=YesPlease and NO_TCLTK=YesPlease. A task-local AUTOM4TE wrapper invokes /usr/bin/perl to work around the host autoconf script’s stale /opt/homebrew/bin/perl shebang. No system tool or global mise config was changed.

License: canonical Apache-2.0 copied from ../kogen/LICENSE; kogen-bench is absent on this machine. No AI attribution.

Inputs (source checkouts remain untouched):
- kogen-spec: main e19dd1c21c19c5be1201c3b6a42c59c28b5c2887; tracked dirty patch SHA256 1fa72f5df8725013ffad3a258d7f851dcb38b187ae06727880b47db2f6b64378.
```text
D quint/.DS_Store
 M quint/prototype/harness/xspec.py
 M quint/slices/approve/golden/hand/13-by-invalid.json
 M quint/slices/approve/scenarios/13-by-invalid.json
 M quint/slices/approve/spec/approve.qnt
 M quint/slices/intent/golden/hand/05-remove.json
 M quint/slices/intent/scenarios/05-remove.json
 M quint/slices/intent/spec/intent.qnt
 M quint/slices/queue/spec/queue.qnt
 M quint/slices/session/golden/hand/06-refusals.json
 M quint/slices/session/golden/hand/07-run-affinity.json
 M quint/slices/session/scenarios/06-refusals.json
 M quint/slices/session/scenarios/07-run-affinity.json
 M quint/slices/session/spec/session.qnt
 M quint/slices/stream/golden/hand/01-switch-after-two-overloads.json
 M quint/slices/stream/golden/hand/02-other-class-resets-streak.json
 M quint/slices/stream/golden/hand/03-attempt-cap-and-budget.json
 M quint/slices/stream/golden/hand/04-wall-bounds-the-retry.json
 M quint/slices/stream/golden/hand/05-upstream-cut-continues.json
 M quint/slices/stream/golden/hand/06-planner-and-fallback-model.json
 M quint/slices/stream/golden/hand/07-usage-and-login-pause.json
 M quint/slices/stream/golden/hand/08-pause-cap-and-shaping.json
 M quint/slices/stream/golden/hand/09-incomplete-and-success.json
 M quint/slices/stream/golden/hand/10-checkpoint-and-refusals.json
 M quint/slices/stream/scenarios/01-switch-after-two-overloads.json
 M quint/slices/stream/scenarios/02-other-class-resets-streak.json
 M quint/slices/stream/scenarios/03-attempt-cap-and-budget.json
 M quint/slices/stream/scenarios/04-wall-bounds-the-retry.json
 M quint/slices/stream/scenarios/05-upstream-cut-continues.json
 M quint/slices/stream/scenarios/06-planner-and-fallback-model.json
 M quint/slices/stream/scenarios/07-usage-and-login-pause.json
 M quint/slices/stream/scenarios/08-pause-cap-and-shaping.json
 M quint/slices/stream/scenarios/09-incomplete-and-success.json
 M quint/slices/stream/scenarios/10-checkpoint-and-refusals.json
 M quint/slices/stream/spec/stream.qnt
 D spec/.DS_Store
?? quint/prototype/harness/test_xspec.py
?? quint/slices/intent/golden/hand/07-shared-base-remove.json
?? quint/slices/intent/scenarios/07-shared-base-remove.json
?? quint/slices/stream/golden/hand/11-auth-capability-and-provider-fallback.json
?? quint/slices/stream/scenarios/11-auth-capability-and-provider-fallback.json
```
- kogen-conformance: v1.3 0f93bad988fb8d7a8eff4e94954d1db0a046c89d; tracked dirty patch SHA256 105ca5fd78684382b4c6ff43ce44c61423458152906ec59c4ce961b515891b85.
```text
M VERSION
 M kogen_conformance/context.py
 M kogen_conformance/runner.py
?? cases/v1.3/
?? data/v1.3/
?? kogen_conformance/contracts.py
?? profiles/v1.3.json
?? quint/
?? reference/results/v1.3/
```
- kogen-rs: main a402540b39cedc7f788472297add7ae2f8a6631a; tracked dirty patch SHA256 e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855.
```text
(clean)
```

The conformance source is now a dirty v1.3 checkout at the same base commit. A read-only committed v1.2 export from ref v1.2 (0f93bad988fb8d7a8eff4e94954d1db0a046c89d) is at ~/cx/kgo/inputs/conformance-v1.2; count verified via runner.load_cases/instances: 236 cases / 570 instances. Never execute the dirty v1.3 checkout for this dispatch.

Queue format follows Rust’s package/deps convention; the Go queue has its own 84 IDs from QUEUE.md, not the Rust queue’s 15 coarse packages. Dependencies are ID aliases resolved to full Go package names. Copied PLAN.md and QUEUE-source.md preserve the full planning inputs.

Validation: GIT_CONFIG_GLOBAL=/dev/null make check passed, then blank HOME with the same empty global config passed. No conformance claim: every production route remains an explicit exit-2 bootstrap stub. Dispatcher syntax and DRY_RUN=1 validated with /bin/bash 3.2; production dispatcher and workers were not started. Full dry-run retained at ~/cx/kgo/dry-run.txt.

State records bind PID/start identity, merged SHA and component/behaviour gate. Fix attempts are bounded by MAX_FIXES (default 2); unresolved branches/worktrees are retained. Never force-remove worktrees or push. Behaviour results require committed evidence and receipt, plus coordinator-owned documented deferral/conflict mapping if needed for early integration rounds. Gaps remain explicit; I8 waits for shared v1.3 and replay admission.

Start command (coordinator only): `MAX=4 ~/cx/kdispatch-go.sh`. The plan’s suggested initial load is `MAX=3`; MAX defaults to 4 as requested.

Go 1.27.1 removes the redundant `toolchain go1.27.1` line when it equals the `go 1.27.1` minimum; retaining it makes vendor/readonly checks request `go mod tidy`. The exact pin is enforced by mise.toml, the Go minimum and the absolute verified check binary with GOTOOLCHAIN=local.

First-commit status at handoff: plain `git commit -m "Bootstrap Go implementation and work queue"` was attempted with the user’s normal Git configuration. GPG pinentry returned `Operation cancelled`; no unsigned fallback, signing change, attribution or push was used. Files are staged and main has no commit yet. A key unlock is required before dispatcher startup.
