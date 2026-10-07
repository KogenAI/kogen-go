package intentapprove

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/approval/publish"
	"kogen-go/internal/approval/remove"
	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/intent"
	"kogen-go/internal/journal"
	"kogen-go/internal/safefs"
	"kogen-go/internal/xspec/protocol"
)

type intentSlice struct {
	world         *world
	digestSymbols map[string]string
	last          string
	exit          int
	did           string
	shown         string
	casTries      int
}

type intentObservation struct {
	Last     string                       `json:"last"`
	Exit     int                          `json:"exit"`
	Did      string                       `json:"did"`
	Shown    string                       `json:"shown"`
	CASTries int                          `json:"casTries"`
	Life     map[string]string            `json:"life"`
	Refs     map[string]intentRefObserved `json:"refs"`
}

type intentRefObserved struct {
	N    int    `json:"n"`
	Hash string `json:"hash"`
}

func (s *intentSlice) Reset(ctx context.Context) error {
	if err := resetWorld(ctx, &s.world, false); err != nil {
		return err
	}
	s.digestSymbols = map[string]string{}
	s.last, s.exit, s.did, s.shown, s.casTries = "ok", 0, "", "", 0
	return nil
}

func (s *intentSlice) HasEventTag(tag string) bool {
	switch tag {
	case "Init", "Shape", "Approve", "Remove", "Adopt":
		return true
	default:
		return false
	}
}

func (s *intentSlice) Apply(ctx context.Context, event protocol.Event) error {
	if ctx == nil {
		return errors.New("intentapprove: context is required")
	}
	if event.Tag == "Init" {
		return s.Reset(ctx)
	}
	value, err := decodeValue(event)
	if err != nil {
		return err
	}
	s.did, s.shown, s.casTries = "", "", 0
	switch event.Tag {
	case "Shape":
		return s.applyShape(ctx, value)
	case "Approve":
		return s.applyApprove(ctx, value)
	case "Remove":
		return s.applyRemove(ctx, value)
	case "Adopt":
		return s.applyAdopt(ctx, value)
	default:
		return fmt.Errorf("intentapprove: unknown intent event %q", event.Tag)
	}
}

func (s *intentSlice) Observe(ctx context.Context) (json.RawMessage, error) {
	observation := intentObservation{
		Last: s.last, Exit: s.exit, Did: s.did, Shown: s.shown, CASTries: s.casTries,
		Life: map[string]string{}, Refs: map[string]intentRefObserved{},
	}
	for _, slug := range []string{"alpha", "bravo"} {
		if life, exists, err := s.observedLife(ctx, slug); err != nil {
			return nil, err
		} else if exists {
			observation.Life[slug] = life
		}
		approval, err := readApprovalPackage(ctx, s.world, slug)
		if err != nil {
			return nil, err
		}
		if approval != nil {
			hash := approval.Hash
			if symbol := s.digestSymbols[approval.Hash]; symbol != "" {
				hash = symbol
			}
			observation.Refs[slug] = intentRefObserved{N: approval.Count, Hash: hash}
		}
	}
	return marshalObservation(observation)
}

func (s *intentSlice) Close() error { return s.world.close() }

func (s *intentSlice) applyShape(ctx context.Context, value eventValue) error {
	slug, err := value.text("slug")
	if err != nil {
		return err
	}
	result, err := value.text("result")
	if err != nil {
		return err
	}
	if !knownSlug(slug) {
		s.setOutcome("unknown_slug", 2)
		return nil
	}
	if result == "valid" {
		if current, err := readApprovalPackage(ctx, s.world, slug); err != nil {
			return err
		} else if current != nil {
			s.last, s.exit, s.did = "ok", 0, "shaped"
			return nil
		}
		if err := s.world.writeSources(slug, validIntent, validAcceptance); err != nil {
			return err
		}
		parsed, parseErr := intent.Parse(slug, validIntent)
		if parseErr != nil {
			s.setOutcome("intent/parse", 1)
			return nil
		}
		for _, issue := range parsed.Lint() {
			if issue.Severity == intent.LintError {
				s.setOutcome("intent/lint", 1)
				return nil
			}
		}
		if err := s.commitShapedSources(ctx, slug); err != nil {
			return err
		}
		s.last, s.exit, s.did = "ok", 0, "shaped"
		return nil
	}
	switch result {
	case "failed":
		s.last, s.exit, s.did = "candidate/repair_limit", 1, "shape_failed"
	case "empty":
		s.setOutcome("intent/request_unavailable", 2)
	case "provider":
		s.setOutcome("provider/overload", 4)
	default:
		s.setOutcome("unknown_result", 70)
	}
	return nil
}

func (s *intentSlice) applyApprove(ctx context.Context, value eventValue) error {
	slug, err := value.text("slug")
	if err != nil {
		return err
	}
	mode, err := value.text("mode")
	if err != nil {
		return err
	}
	symbol, err := value.text("hash")
	if err != nil {
		return err
	}
	race, err := value.text("race")
	if err != nil {
		return err
	}
	if !knownSlug(slug) {
		s.setOutcome("unknown_slug", 2)
		return nil
	}
	if mode != "card" && mode != "commit" {
		s.setOutcome("unknown_mode", 70)
		return nil
	}
	raceCount := raceAttempts(race)
	if raceCount < 0 {
		s.setOutcome("unknown_race", 70)
		return nil
	}
	if err := s.world.setControls("green", "green", "green"); err != nil {
		return err
	}
	deps := s.world.prepareDependencies()
	request := prepare.Request{
		Project: s.world.project, Slug: slug, RunDir: s.world.runDir,
		BaseEnvironment: cloneProcessEnvironment(s.world.env),
		Toolchain:       map[string]string{"fixture": "intentapprove-v1"}, ToolchainKnown: true,
		AdapterVersion: "intentapprove-v1",
	}
	if err := s.world.clearMiseProbeLog(); err != nil {
		return err
	}
	var actualClaim string
	if mode == "commit" {
		actualClaim = "000000"
		intentBytes, acceptanceBytes, exists, readErr := currentSources(ctx, s.world, slug)
		if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
			return readErr
		}
		if exists {
			actual := intent.ApprovalSHA256(intentBytes, acceptanceBytes)
			actualClaim, _, err = intentClaim(actual, symbol)
			if err != nil {
				return err
			}
		}
		request.HashPrefix = actualClaim
	}
	prepared, prepErr := prepare.Prepare(ctx, request, deps)
	if prepErr != nil {
		s.last, s.exit = prepareOutcome(prepErr)
		if s.last == "intent/hash_mismatch" {
			s.shown = symbol
		}
		return nil
	}
	if mode == "card" {
		s.last, s.exit, s.did, s.shown = "needs_decision", 5, "card", symbol
		if prepared != nil {
			s.digestSymbols[prepared.ApprovalSHA256] = symbol
		}
		return nil
	}
	if prepared == nil {
		return errors.New("intentapprove: preparation returned no approval")
	}
	if _, err := gitio.ParseObjectID(string(prepared.BaseCommit)); err != nil {
		return err
	}
	lateBytes := []byte(nil)
	if lateIntent, ok := value["lateIntentBytes"]; ok {
		lateBytes, err = rawBytes(lateIntent)
		if err != nil {
			return err
		}
	} else if lateAcceptance, ok := value["lateAcceptanceBytes"]; ok {
		lateBytes, err = rawBytes(lateAcceptance)
		if err != nil {
			return err
		}
	}
	casGit := &racingGit{base: deps.Git, world: s.world, slug: slug, remaining: raceCount}
	deps.Git = casGit
	if lateBytes != nil {
		latePath := prepared.AcceptanceSourcePath
		if _, ok := value["lateIntentBytes"]; ok {
			latePath = fmt.Sprintf(intentPathFormat, slug)
		}
		deps.Roots = &lateWriteOpener{base: safefs.Opener{}, checkout: s.world.checkout, name: latePath, bytes: lateBytes}
	}
	result, publishErr := publish.Publish(ctx, publish.Request{
		Project: s.world.project, Prepared: prepared, GivenHashPrefix: actualClaim, At: fixtureTimestamp,
	}, publish.Dependencies{Git: deps.Git, Policy: func(directory string) contract.GitPolicy { return s.world.policy(directory) }, Roots: deps.Roots})
	s.casTries = casGit.attempts
	if publishErr != nil {
		s.last, s.exit = publishOutcome(publishErr)
		return nil
	}
	if result == nil {
		return errors.New("intentapprove: publisher returned no result")
	}
	s.digestSymbols[prepared.ApprovalSHA256] = symbol
	s.last, s.exit, s.did, s.shown = "ok", 0, "approved", symbol
	return nil
}

func (s *intentSlice) applyRemove(ctx context.Context, value eventValue) error {
	slug, err := value.text("slug")
	if err != nil {
		return err
	}
	force, err := value.boolean("force", false)
	if err != nil {
		return err
	}
	if !knownSlug(slug) {
		s.setOutcome("unknown_slug", 2)
		return nil
	}
	_, err = remove.Remove(ctx, remove.Request{Project: s.world.project, Slug: slug, Force: force}, remove.Dependencies{
		Git: s.world.git, Policy: func(directory string) contract.GitPolicy { return s.world.policy(directory) }, Roots: safefs.Opener{},
	})
	if err != nil {
		s.last, s.exit = removeOutcome(err)
		return nil
	}
	s.last, s.exit, s.did = "ok", 0, "removed"
	return nil
}

func (s *intentSlice) applyAdopt(ctx context.Context, value eventValue) error {
	slug, err := value.text("slug")
	if err != nil {
		return err
	}
	status, err := value.text("status")
	if err != nil {
		return err
	}
	if !knownSlug(slug) {
		s.setOutcome("unknown_slug", 2)
		return nil
	}
	if status != "building" && status != "failed" && status != "parked" && status != "interrupted" && status != "landed" {
		s.setOutcome("bad_adopt", 70)
		return nil
	}
	approval, err := readApprovalPackage(ctx, s.world, slug)
	if err != nil {
		return err
	}
	if approval == nil {
		return errors.New("intentapprove: Adopt requires an actual approval package")
	}
	runID := runIDForSlug(slug)
	if status == "landed" {
		if err := s.landSources(ctx, slug); err != nil {
			return err
		}
	}
	if err := s.writeRunState(runID, slug, status, approval); err != nil {
		return err
	}
	if status == "building" {
		if err := s.publishClaim(ctx, runID); err != nil {
			return err
		}
	} else if err := s.releaseClaim(ctx, runID); err != nil {
		return err
	}
	s.last, s.exit, s.did = "ok", 0, "adopted"
	return nil
}

func (s *intentSlice) observedLife(ctx context.Context, slug string) (string, bool, error) {
	approval, err := readApprovalPackage(ctx, s.world, slug)
	if err != nil {
		return "", false, err
	}
	if approval != nil {
		if status, exists, err := s.adoptedLife(ctx, slug, approval); err != nil {
			return "", false, err
		} else if exists {
			return status, true, nil
		}
		return "approved", true, nil
	}
	root, err := safefs.OpenRoot(s.world.checkout)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer root.Close()
	intentBytes, err := root.ReadFile(fmt.Sprintf(intentPathFormat, slug))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if _, err := intent.Parse(slug, intentBytes); err != nil {
		return "", false, nil
	}
	return "shaped", true, nil
}

func (s *intentSlice) adoptedLife(ctx context.Context, slug string, approval *approvalPackage) (string, bool, error) {
	runID := runIDForSlug(slug)
	root, err := safefs.OpenRoot(s.world.stateRoot)
	if err != nil {
		return "", false, err
	}
	defer root.Close()
	store, err := journal.NewRunStore(root, "runs/"+runID)
	if err != nil {
		return "", false, err
	}
	snapshot, err := store.ReadSnapshot()
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if snapshot.Slug != slug || snapshot.ApprovalCommit != string(approval.Target) || snapshot.ApprovalSHA256 != approval.Hash {
		return "", false, nil
	}
	events, err := store.ReadEvents()
	if errors.Is(err, fs.ErrNotExist) {
		events = nil
	} else if err != nil {
		return "", false, err
	}
	lastEvent := ""
	if len(events) != 0 {
		lastEvent = events[len(events)-1].Event
	}
	switch snapshot.Status {
	case "landed":
		return "landed", true, nil
	case "failed":
		if lastEvent == "interrupted" {
			return "interrupted", true, nil
		}
		return "failed", true, nil
	case "parked":
		return "parked", true, nil
	case "running":
		refs := gitio.NewRefPort(s.world.git, s.world.policy(s.world.origin))
		claim, err := refs.ReadRef(ctx, "refs/kogen/claim")
		if err != nil {
			return "", false, err
		}
		if claim.Exists {
			owner, err := claimRunID(ctx, s.world, claim.Target)
			if err != nil {
				return "", false, err
			}
			if owner == runID {
				return "building", true, nil
			}
		}
		if lastEvent == "interrupted" {
			return "interrupted", true, nil
		}
		return "approved", true, nil
	default:
		return "", false, nil
	}
}

func (s *intentSlice) writeRunState(runID, slug, status string, approval *approvalPackage) error {
	root, err := safefs.OpenRoot(s.world.stateRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	store, err := journal.NewRunStore(root, "runs/"+runID)
	if err != nil {
		return err
	}
	snapshot, err := store.ReadSnapshot()
	if errors.Is(err, fs.ErrNotExist) {
		snapshot = journal.RunSnapshot{
			Schema: 2, RunID: runID, Slug: slug, ApprovalSHA256: approval.Hash,
			ApprovalCommit: string(approval.Target), TargetBranch: "main", Status: adoptedJournalStatus(status),
			OwnerPID: int64(os.Getpid()), OwnerStartedMS: 1, StartedMS: 100,
			Recovery: []journal.RecoveryRecord{},
		}
		if err := store.Create(snapshot); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if snapshot.Slug != slug || snapshot.ApprovalSHA256 != approval.Hash || snapshot.ApprovalCommit != string(approval.Target) {
		return errors.New("intentapprove: run journal does not match the actual approval")
	}
	snapshot.Status = adoptedJournalStatus(status)
	eventName, reason := adoptedJournalEvent(status)
	event := journal.NewRunEvent(eventName, 100)
	if reason != "" {
		if err := event.Set("reason", reason); err != nil {
			return err
		}
	}
	return store.Record(event, snapshot)
}

func adoptedJournalStatus(status string) string {
	if status == "building" || status == "interrupted" {
		return "running"
	}
	return status
}

func adoptedJournalEvent(status string) (name, reason string) {
	switch status {
	case "building":
		return "started", ""
	case "failed":
		return "finished", "repair_limit"
	case "parked":
		return "parked", ""
	case "interrupted":
		return "interrupted", "interrupted"
	case "landed":
		return "landed", ""
	default:
		return "adopted", ""
	}
}

func runIDForSlug(slug string) string {
	digest := sha256.Sum256([]byte("intentapprove-run:" + slug))
	return hex.EncodeToString(digest[:16])
}

func (s *intentSlice) publishClaim(ctx context.Context, runID string) error {
	refs := gitio.NewRefPort(s.world.git, s.world.policy(s.world.origin))
	current, err := refs.ReadRef(ctx, "refs/kogen/claim")
	if err != nil {
		return err
	}
	if current.Exists {
		owner, err := claimRunID(ctx, s.world, current.Target)
		if err != nil {
			return err
		}
		if owner == runID {
			return nil
		}
		return errors.New("intentapprove: active claim belongs to another run")
	}
	if err := s.world.writeCheckout(".kogen/claim", []byte(runID+"\n"), 0o600); err != nil {
		return err
	}
	if err := s.world.gitOK(ctx, s.world.checkout, "add", "--", ".kogen/claim"); err != nil {
		return err
	}
	if err := s.world.gitOK(ctx, s.world.checkout, "commit", "--quiet", "-m", "xspec active claim"); err != nil {
		return err
	}
	if err := s.world.gitOK(ctx, s.world.checkout, "push", "--quiet", s.world.origin, "HEAD:refs/kogen/claim"); err != nil {
		return err
	}
	return s.world.gitOK(ctx, s.world.checkout, "reset", "--quiet", "--hard", "HEAD^")
}

func (s *intentSlice) releaseClaim(ctx context.Context, runID string) error {
	refs := gitio.NewRefPort(s.world.git, s.world.policy(s.world.origin))
	current, err := refs.ReadRef(ctx, "refs/kogen/claim")
	if err != nil || !current.Exists {
		return err
	}
	owner, err := claimRunID(ctx, s.world, current.Target)
	if err != nil {
		return err
	}
	if owner != runID {
		return errors.New("intentapprove: refusing to release a claim owned by another run")
	}
	result, err := refs.CompareAndSwap(ctx, contract.RefUpdate{Name: "refs/kogen/claim", Expected: current.Target})
	if err != nil {
		return err
	}
	if !result.Updated {
		return errors.New("intentapprove: active claim changed before release")
	}
	return nil
}

func claimRunID(ctx context.Context, w *world, target contract.ObjectID) (string, error) {
	result, err := w.git.Exec(ctx, []string{"cat-file", "blob", string(target) + ":.kogen/claim"}, nil, w.policy(w.origin))
	if err != nil {
		return "", err
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 {
		return "", errors.New("intentapprove: claim ref target has no claim blob")
	}
	owner := strings.TrimSpace(string(result.Stdout))
	if len(owner) != 32 {
		return "", errors.New("intentapprove: claim blob has an invalid run ID")
	}
	return owner, nil
}

func (s *intentSlice) landSources(ctx context.Context, slug string) error {
	if _, _, exists, err := currentSources(ctx, s.world, slug); err != nil {
		return err
	} else if !exists {
		return errors.New("intentapprove: cannot land missing Intent source bytes")
	}
	if err := s.world.gitOK(ctx, s.world.checkout, "push", "--quiet", s.world.origin, "HEAD:refs/heads/main"); err != nil {
		return err
	}
	return nil
}

func (s *intentSlice) commitShapedSources(ctx context.Context, slug string) error {
	if err := s.world.gitOK(ctx, s.world.checkout, "add", "--", fmt.Sprintf(intentPathFormat, slug), fmt.Sprintf(acceptancePathFormat, slug)); err != nil {
		return err
	}
	if err := s.world.gitOK(ctx, s.world.checkout, "commit", "--quiet", "-m", "xspec shaped Intent"); err != nil {
		return err
	}
	return nil
}

func (s *intentSlice) setOutcome(code string, exit int) { s.last, s.exit = code, exit }

func (s *intentSlice) String() string { return "intent xspec production slice" }

func knownSlug(slug string) bool { return slug == "alpha" || slug == "bravo" }

func prepareOutcome(err error) (string, int) {
	var failure *prepare.Failure
	if errors.As(err, &failure) {
		code := failure.Reason
		switch code {
		case "parse":
			code = "intent/parse"
		case "lint":
			code = "intent/lint"
		case "not_found":
			code = "intent/not_found"
		case "acceptance_missing":
			code = "intent/acceptance_missing"
		case "approval_identity_unavailable":
			code = "intent/approval_identity_unavailable"
		case "approval_by_invalid":
			code = "intent/approval_by_invalid"
		case "acceptance_check_failed":
			code = "check/acceptance_check_failed"
		case "tool_missing":
			code = "environment/tool_missing"
		case "setup_failed":
			code = "environment/setup_failed"
		default:
			if !strings.Contains(code, "/") {
				code = "environment/" + code
			}
		}
		return code, int(failure.Exit)
	}
	return "environment/approval_check_failed", 3
}

func publishOutcome(err error) (string, int) {
	var failure *publish.Failure
	if errors.As(err, &failure) {
		switch failure.Reason {
		case "hash_mismatch":
			return "intent/hash_mismatch", 1
		case "approval_ref_conflict":
			return "controller/approval_cas_lost", 70
		default:
			return "environment/approval_publish_failed", 3
		}
	}
	return "environment/approval_publish_failed", 3
}

func removeOutcome(err error) (string, int) {
	var failure *remove.Failure
	if errors.As(err, &failure) {
		code := failure.Reason
		if failure.Class != "" && failure.Class != "intent" {
			code = string(failure.Class) + "/" + code
		} else {
			code = "intent/" + code
		}
		return code, int(failure.Exit)
	}
	return "environment/remove_failed", 3
}

var _ protocol.Slice = (*intentSlice)(nil)
var _ = fs.ErrNotExist
