# 41-single-rung-controller evidence

## Revision and gate

- Worker package revision: `fcdc7b6` (`Implement single-rung build controller`), based on `72fc4b54519a0e9e3135cb6fe0e55d9eb121cd80`.
- CLI revision: `fcdc7b6`; `cmd/kogen` was not changed and still reports `implementation bootstrap; command routes are not wired` for command routing.
- Adapter revision: no production Build coordinator/CLI adapter is wired in this worktree. The controller exposes typed ports for approval, claim, provider stages, workspace, verification, base refresh, landing, and candidate preservation.
- Gate: **component**. The black-box behavior gate remains open.

## Commands and results

Pinned worker tool path used before commands:

```sh
export PATH="$HOME/.local/share/mise/installs/git/2.54.0/bin:$HOME/.local/share/mise/installs/go/1.27.1/bin:$HOME/.local/share/mise/installs/python/3.14.7/bin:$HOME/.local/share/mise/installs/node/24.21.0/bin:$PATH"
```

Commands run:

```sh
gofmt -w internal/build/single/*.go
go test ./internal/build/single
GIT_CONFIG_GLOBAL=/dev/null make check
make build
```

- `go test ./internal/build/single`: passed, including `TestInvalidApprovalStopsBeforeClaimAndProvider`.
- `GIT_CONFIG_GLOBAL=/dev/null make check`: passed format, vendor fingerprints, vet, tests, and both builds.
- `make build`: passed.

The assigned frozen v1.2 command was run once, unchanged:

```sh
SUITE="$HOME/cx/kgo/inputs/conformance-v1.2"
EVIDENCE="$HOME/cx/kgo/evidence/41-single-rung-controller"
mkdir -p "$EVIDENCE"
PYTHONDONTWRITEBYTECODE=1 PATH="$HOME/.local/share/mise/installs/python/3.14.7/bin:$PATH" \
  "$SUITE/bin/kogen-conformance" run --kogen "$PWD/bin/kogen" \
  --profile cli,state,approval,shape,build,ladder,provider,custody,format,v1.2 --case 'build-39,build-40,build-41,build-42,state-10,v1.2-130-state-11,v1.2-131-state-12,v1.2-37-build-02,v1.2-38-build-03,v1.2-43-build-08,v1.2-44-build-09,v1.2-67-build-43' \
  --jobs 2 --time-scale 0.02 --workdir "$EVIDENCE/work" \
  --out "$EVIDENCE/results.jsonl"
```

The suite resolved 12 literal cases, one instance each, on `macOS-26.7.1-arm64-arm-64bit-Mach-O`, with suite version `v1.2+unknown`:

| Case IDs | Instances | Pass | Fail | Error |
| --- | ---: | ---: | ---: | ---: |
| `state-10` | 1 | 0 | 1 | 0 |
| `build-39`, `build-40`, `build-41`, `build-42` | 4 | 0 | 4 | 0 |
| `v1.2-130-state-11`, `v1.2-131-state-12`, `v1.2-37-build-02`, `v1.2-38-build-03`, `v1.2-43-build-08`, `v1.2-44-build-09`, `v1.2-67-build-43` | 7 | 0 | 7 | 0 |

There are **no compatible behavior passes** in this oracle run. Every case failed in its setup at `kogen intent approve greet {hash8:greet}`: exit 2 instead of 0, empty stdout, and stderr `kogen: implementation bootstrap; command routes are not wired`. The fixture assertions were not reached. The build fixtures also report that no request reached the fake server (`KOGEN_PROVIDER_URL seam missing?`). This is an integration/wiring gap, not an observed v1.2-versus-draft semantic conflict. The failure run and per-case work directories are retained at:

`/Users/almirsarajcic/cx/kgo/evidence/41-single-rung-controller/results.jsonl`

## Implemented component effects

- Approval loading resolves a direct approval ref and exact package blobs, checks schema, digests, target, domains, check baseline, protected manifest, and acceptance paths. The controller repeats the byte-digest and Intent checks before claim acquisition. The package test confirms malformed approval stops before claim and provider calls.
- The controller takes an origin claim before run creation, persists one run cache affinity key, plans before creating R1, uses one conversation object for each planner/builder stage, installs approved files in a private workspace, restores and checks protection, verifies the final workspace, then invokes commit and the moved-base/CAS landing port.
- Capped development completions continue to verification. Non-landed workspaces are preserved as unverified candidate refs before cleanup. Terminal cleanup and claim-release failures become typed failures and are recorded as cleanup-pending where the journal is available.
- Git approval, claim, and candidate effects go through supervised Git ports; state, run journal, workspace, and plan publications use rooted filesystem APIs.
- These production ports were compiled and covered by repository checks, but no CLI flow reached them during the oracle run.

## Conflicts, replay, and closure

- Exact v1.2/draft semantic conflicts observed: none. No frozen v1.3 D-* case IDs were available, so none are claimed.
- R(slice) was not run: no shared coherent migrated Quint cohort was available. Seeds `17`, `23`, and `41` and their `500 × 25` traces were not exercised; divergence is unmeasured.
- Linux runtime evidence, optional-runtime evidence, and live-comparison evidence were not available in this worker run.
- Behavior closure requires I3 integration and a rerun of the selected frozen cases after integration/rebase. The executable path must demonstrate approve → queue → provider → gate → CAS → status.
- Package 00 remains a real foundation task after this bootstrap.
- Release closure also requires shared frozen v1.3 IDs, the shared v1.3 manifest and production replay manifest where applicable, the prescribed R(slice) evidence, and the stated Linux/optional-runtime/live-comparison evidence. No gate policy or shared suite was changed by this worker.
