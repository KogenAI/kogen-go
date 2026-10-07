# Adversarial safety matrix

Run `tools/safety-matrix.sh` from the assigned Go checkout. It uses the pinned
Go on `PATH`, sets `GOMAXPROCS=2`, disables global Git configuration for test
children, and acquires `$HOME/cx/kgo/gates.lock/custody` before running the
filesystem, Git, recovery, and landing fixtures. It releases only the lock it
created. An existing lock is reported as a blocked run and is never removed.

The matrix exercises real temporary Git repositories and rooted filesystem
operations:

| Surface | Effects checked |
| --- | --- |
| Publication | Replacing a symlink leaf leaves its target alone; hardlinked leaves are refused without changing either name; FIFO reads and replacement fail promptly. Existing safefs race coverage also swaps a parent directory with an outside symlink during publication. |
| Workspace Git | Candidate capture uses native nested ignore and negation rules, retains base-tracked paths after a new ignore rule, excludes ignored untracked files, and disregards `.git/info/exclude`, global excludes, filters, fsmonitor and hooks. It uses the frozen base despite builder HEAD/index changes and rejects replaced `.git` metadata. |
| Signing | A temporary origin policy points `gpg.program` at a deliberately hanging signer. The supervised Git call must time out promptly after the signer starts. No developer Git configuration or signing identity is changed. |
| Recovery publication | Cross-package fixtures publish a create-only recovery ref, repeat preservation without writing the run record to exercise crash adoption, and preserve later changed bytes under a distinct archive identity while retaining the original ref and source workspace. A real base CAS followed by later workspace edits exercises the production recovery controller and keeps those edits unverified while the landing stays landed. |
| D3 controller recovery | Existing owner tests inject ref and archive publication failures, require workspace and candidate retention with `cleanup_pending`, retry terminal cleanup without changing the outcome, and preserve later edits after post-CAS reconciliation. They also cover a live owner no-op and exact tracked/untracked/mode/symlink state. |

The script runs only the listed adversarial and owner-level tests. It is a
component regression matrix, not the I6 behaviour gate. It does not prove Linux
parity when run on macOS, live-provider behaviour, optional Rails/ExUnit
availability, or the planned D-REC-01–06 acceptance cases. Those draft fixtures
have no shared frozen v1.3 IDs yet; report them as unavailable rather than
counting these local tests as frozen-case passes.
