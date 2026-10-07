# 11-child-environment evidence

## Revisions and environment

- Worker implementation commits: `db3c18b9f9378872f9712e3add3b073a86390fbd` and `37a3340c82cb10545ed30e1c284b92f05e9b7ed3` on `kgo/11-child-environment`; final source is the latter.
- CLI source at the oracle run: commit `540cb346f8bff8bca50f7c7f6af5cd2d69f6b0ff`, `cmd/kogen/main.go` blob `8dd7e182c9b607a3d1e6bd928514823fdacdb234`. The file still returns `kogen: implementation bootstrap; command routes are not wired`.
- Production adapter revision: none. No route imports or calls `internal/process` yet.
- Target spec: `kogen-spec` `e19dd1c21c19c5be1201c3b6a42c59c28b5c2887` (v1.3-draft). Read `spec/02-formats.md` §§2.3 and 2.9, `spec/05-sandbox-custody.md` §§5.1–5.2, and `spec/CONFORMANCE.md` C.3. `CHANGES-v1.3.md` §§2, 6–7 leave cache identity and cross-session provider-prefix work to their owning packages; they do not change this package's environment merge order.
- Frozen suite: `kogen-conformance` `0f93bad988fb8d7a8eff4e94954d1db0a046c89d` (`v1.2+unknown` runner metadata). The copied input directory is not a Git checkout. The runner reported macOS 26.7.1, arm64, and Git 2.54.0.
- Rust reference: `kogen-rs` `a402540b39cedc7f788472297add7ae2f8a6631a`; inspected `run/environment.rs`, `run/environment_tests.rs`, `run/process.rs`, and Git landing environment policy in `git/landing/gitops.rs` and `commit.rs`.
- Worker tools: Go `go1.27.1 darwin/arm64`, Git `2.54.0`; the required pinned mise tool paths were prepended for every worker command.

## Implementation and local effects

`BuildChildEnvironment` applies the §5.2 order: allowlisted host values, run-local `TMPDIR`, optional unconfined `mise env -C <workspace> --json --quiet` supervised for 30 seconds, then project values. `mise-state`, `mise-cache`, `tmp`, and `logs` are created through the supplied rooted run-directory capability and must be owner-only, non-symlink directories. The probe log is read back through that rooted capability, bounded to 1 MiB, and parsed as a JSON string map. Inherited and mise-returned trusted paths are preserved with the project root and workspace; Kogen runtime directories are removed from child `PATH`; the mise installation directory is prepended. A project `PATH` is applied last without modification.

`ControllerGitEnvironment` copies the controller's base environment separately from project child overrides, preserving production global identity, signing, and Git config settings. Hermetic test Git policy remains in `internal/testkit` with isolated global/system config, local identity, and signing disabled. `SetupKeyEnvironment` omits `MISE_STATE_DIR`, `MISE_CACHE_DIR`, and `MISE_TRUSTED_CONFIG_PATHS` from setup-key environment material while retaining `TMPDIR` and other constructed child values, matching the literal v1.3-draft §2.9 definition.

Local unit fixtures created private temporary directories and an executable shell stub. Tests checked the complete child environment and mise request, an actual `Supervisor`-run JSON probe, project precedence, filtered host variables, controller/test Git separation, mise failure and malformed/oversized output, symlink refusal, and the draft cache projection: mise state/trust paths are excluded while `TMPDIR` remains. No real mise installation, provider, account, user checkout, or origin was accessed.

## Commands and results

All commands used the worker-pinned PATH.

| Command | Result |
|---|---|
| `GOMAXPROCS=2 GOFLAGS='-mod=vendor -p=2' GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -count=1 -parallel=2 ./internal/process` | Passed on final source. |
| `GIT_CONFIG_GLOBAL=/dev/null GOMAXPROCS=2 make check` | Passed on final source: format, vendor fingerprints, vet, full tests with `-parallel=2`, and both builds. |
| `make build` | Built `bin/kogen` and `bin/kogen-xspec`. |
| Assigned frozen v1.2 command below | Runner exit 1; 5 selected IDs / 7 instances, 0 passed, 5 failed cases, 0 errors, 0 skipped, 0 unimplemented. The run is retained at `/Users/almirsarajcic/cx/kgo/evidence/11-child-environment/results.jsonl`; workdirs are under `/Users/almirsarajcic/cx/kgo/evidence/11-child-environment/work`. |
| `git diff --cached --check` | Passed before the implementation commit. |

The assigned command ran at `2026-10-07T18:36:18Z` before implementation commits `db3c18b` and `37a3340`. The subsequent source changes were confined to environment construction and setup-key projection; they were covered by focused tests and final `make check`. The compiled CLI did not import the environment package, and its bootstrap diagnostic confirms the integration boundary. The oracle failure was retained and was not rerun or replaced.

```sh
make build
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/11-child-environment"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'approval-12,approval-13,state-03,state-16,v1.2-133-state-26' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

Resolved IDs and actual instances:

| ID | Instances | Result and observed failure |
|---|---:|---|
| `state-03` | 1 | Failed at `kogen status`: exit 2, expected 0; stderr was `kogen: implementation bootstrap; command routes are not wired`. |
| `state-16` | 3 (`1:env KOGEN_SANDBOXED`, `2:scalar domain`, `3:duplicate check`) | All failed at `kogen status`: exit 2, expected 3; same bootstrap stderr. |
| `approval-12` | 1 | Failed at `kogen intent approve greet`: exit 2, expected 5; same bootstrap stderr. |
| `approval-13` | 1 | Failed at `kogen intent approve greet`: exit 2, expected 3; same bootstrap stderr. |
| `v1.2-133-state-26` | 1 | Failed at approval: exit 2, expected 0; same bootstrap stderr. The runner hint confirms no provider request reached the fake server. |

No selected instance reached project-config validation, approval checks, mise, setup-key generation, or a provider. The oracle did not observe the cache-key assertion. A textual historical conflict is exact: v1.2 case `v1.2-133-state-26` requires the setup key not to change with `TMPDIR` or `MISE_STATE_DIR`; target v1.3-draft `spec/02-formats.md` §2.9 defines setup `child_env` without `MISE_STATE_DIR`, `MISE_CACHE_DIR`, or `MISE_TRUSTED_CONFIG_PATHS`, but does not exclude `TMPDIR`. Thus the conflict is `TMPDIR` only; `MISE_STATE_DIR` remains excluded. The implementation follows the draft wording. The package component test covers this projection, but this command does not count V133 as passing.

## Closure gates and gaps

- **I1 behavior remains open.** The project loader, approval route, CLI handler, and production adapter are not wired. Rerun all selected IDs after integration; preserve this bootstrap failure in the run history.
- **I5 cache behavior remains open.** `internal/setupcache` is still a stub. Package 54 must use the environment projection and implement the v2 setup key, independent v3 verification-baseline key, and cache replay. V133 and A12–13 are not accepted behavior evidence here. Package 00/coordinator must carry the exact `TMPDIR` historical conflict into the shared v1.3 closure policy; this worker did not edit that policy.
- **No shared frozen v1.3 suite or migrated draft fixtures are available.** Planned D-* cases remain unavailable v1.2 cases; wait for shared frozen IDs before making draft claims. No coherent migrated Quint cohort was available, so no `R(slice)` run or seeds 17/23/41 × 500 traces ×25 steps are claimed.
- **Platform/runtime gates remain open.** Verification ran on macOS only; no Linux runtime evidence, optional runtime evidence, or live comparison evidence was produced.
- The component commit is code-ready evidence only. Worker exit and `make check` do not confer behavior acceptance.
