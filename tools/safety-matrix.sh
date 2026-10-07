#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$root"

case $(uname -s) in
	Darwin|Linux) ;;
	*) printf '%s\n' "safety matrix requires a Darwin or Linux host" >&2; exit 2 ;;
esac

lock_root=${HOME:?HOME must be set}/cx/kgo/gates.lock
lock="$lock_root/custody"
mkdir -p "$lock_root"
if ! mkdir "$lock" 2>/dev/null; then
	printf '%s\n' "safety matrix lock is already held: $lock" >&2
	exit 75
fi
release_lock() { rmdir "$lock"; }
trap release_lock EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

run_group() {
	label=$1
	pattern=$2
	package=$3
	printf '\n%s\n' "$label"
	GOMAXPROCS=2 GIT_CONFIG_GLOBAL=/dev/null go test -count=1 -p=2 -parallel=2 -run "$pattern" "$package"
}

run_group \
	"Cross-package publication, Git poisoning, signing timeout and recovery effects" \
	'^TestAdversarial' \
	'./internal/testkit'

run_group \
	"Rooted publication: symlink, hardlink, FIFO and parent replacement" \
	'^(TestPublishReplacesSymlinkLeafWithoutTouchingTarget|TestRootRejectsFIFOAndDeviceFilesWithoutBlocking|TestAppendRefusesHardlinkAliases|TestPublishPrivateCreateOnlyAndHardlinkRefusal|TestParentSymlinkReplacementRaceCannotRedirectPublication)$' \
	'./internal/safefs'

run_group \
	"Candidate tree: native ignores, poisoned Git config, immutable base, exact modes and unsafe .git replacement" \
	'^(TestCandidateTreeUsesNativeNestedIgnoreRulesAndBaseTrackedPaths|TestCandidateTreeIgnoresBuilderHeadAndIndexAndPreservesGitModes|TestCandidateTreeRejectsReplacedGitDirectory|TestWriteExactTreeIncludesIgnoredApprovalBytesAndPreservesModes)$' \
	'./internal/gitio'

run_group \
	"D3 preservation: latest bytes, adoption, changed later work, failed ref/archive and terminal retry" \
	'^(TestPreserveCapturesLatestBaseRelativeBytesModesAndSymlinks|TestCreateOnlyRefAdoptionAndLaterWorkGetSeparateArchiveIdentity|TestRefPublicationFailureFallsBackToAdoptableLosslessArchive|TestGitTreeFailureStillArchivesWorkspaceWithoutLosingIgnoredFiles|TestBothPublicationFailuresLeaveWorkspaceAndPriorCandidateRef|TestControllerRetainsPendingWorkspaceOnDualFailureAndRetriesTerminalCleanup)$' \
	'./internal/recovery/preserve'

run_group \
	"Recovery: post-CAS preservation, immutable terminal outcome and live-owner no-op" \
	'^(TestRecoverPostCASPreservesBeforeCleanupAndReconcilesLanded|TestFailedPreservationRetainsWorkspaceAndTerminalRetryDoesNotRetryBuild|TestLiveOwnerIsUntouched)$' \
	'./internal/recovery'

run_group \
	"Landing: crash after base CAS retains recovery boundary" \
	'^TestPublishCrashBoundaryAfterBaseCASLeavesIncomingForRecovery$' \
	'./internal/landing/publish'
