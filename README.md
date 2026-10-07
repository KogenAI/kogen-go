# Kogen Go

Bootstrap of the Go implementation of the Kogen v1.3 draft. One local module, `kogen-go`, with public `cmd/kogen` and private `cmd/kogen-xspec` entrypoints. Command routes are deliberately unavailable until their integration rounds.

Use `mise exec -- make check` for offline format, vet, tests and binary builds; `make build` writes binaries to `bin/`. Go 1.27.1, Python 3.14.7, Node 24.21.0, Quint 0.33.0 and Git 2.54.0 are pinned locally by mise. The sole Go dependency is vendored `golang.org/x/sys v0.48.0`.

The 84 task inputs and ownership rules are in `docs/work/`. The dispatcher is `~/cx/kdispatch-go.sh`; it has not been started. `DRY_RUN=1 ~/cx/kdispatch-go.sh` prints dependency waves. State, evidence and logs are under `~/cx/kgo/`.

The authoritative spec is `../kogen-spec` at e19dd1c with `CHANGES-v1.3.md`. The oracle is the committed v1.2 snapshot under `~/cx/kgo/inputs/conformance-v1.2`, because the source checkout has v1.3 work in progress. Rust is a structural reference. Historical v1.2 conflicts with the draft must be reported; this scaffold claims no conformance or release acceptance. Apache-2.0; canonical license copied from Kogen (kogen-bench is absent on this host).
