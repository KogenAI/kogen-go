#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
GO=${KGO_GO:-go}
export GOTOOLCHAIN=local GOWORK=off GOFLAGS='-mod=vendor -p=2' GOPROXY=off GOSUMDB=off
export GOMAXPROCS=${GOMAXPROCS:-2}
[ "$("$GO" version | cut -d ' ' -f 3)" = go1.27.1 ] || { echo 'Go 1.27.1 required' >&2; exit 1; }
GO_PATH=$(command -v "$GO")
GOBIN=$(dirname "$GO_PATH")
GIT_BIN=${KGO_GIT:-git}
[ "$("$GIT_BIN" --version)" = 'git version 2.54.0' ] || { echo 'Git 2.54.0 required' >&2; exit 1; }
GIT_PATH=$(command -v "$GIT_BIN")
export PATH="$(dirname "$GIT_PATH"):$PATH"
bad=$(find cmd internal tools -name '*.go' -type f -exec "$GOBIN/gofmt" -l {} +)
[ -z "$bad" ] || { echo "gofmt required: $bad" >&2; exit 1; }
shasum -a 256 -c docs/work/VENDOR.sha256 >/dev/null
before=$(git status --porcelain --untracked-files=all; git diff --binary HEAD 2>/dev/null || :)
"$GO" vet ./...
"$GO" test -count=1 -parallel=2 ./...
out=$(mktemp -d "${TMPDIR:-/tmp}/kgo-check.XXXXXX")
trap 'rm -rf "$out"' 0 HUP INT TERM
CGO_ENABLED=0 "$GO" build -trimpath -o "$out/kogen" ./cmd/kogen
CGO_ENABLED=0 "$GO" build -trimpath -o "$out/kogen-xspec" ./cmd/kogen-xspec
after=$(git status --porcelain --untracked-files=all; git diff --binary HEAD 2>/dev/null || :)
[ "$before" = "$after" ] || { echo 'check changed repository sources' >&2; exit 1; }
echo 'check: format, vendor fingerprints, vet, tests and both builds passed'
