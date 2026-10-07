package intentapprove

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/approval/publish"
	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/intent"
	"kogen-go/internal/safefs"
	"kogen-go/internal/xspec/protocol"
)

type approveSlice struct {
	world         *world
	last          string
	exit          int
	sha8          string
	approver      string
	feasibility   string
	bwarn         bool
	lwarn         bool
	ran           bool
	lastCache     [2]string
	digestSymbols map[string]string
	commitSymbols map[contract.ObjectID]string
	baseSymbols   map[contract.ObjectID]string
	applyErr      error
}

type approveObservation struct {
	Last      string                        `json:"last"`
	Exit      int                           `json:"exit"`
	SHA8      string                        `json:"sha8"`
	Approver  string                        `json:"approver"`
	Feas      string                        `json:"feas"`
	BWarn     bool                          `json:"bwarn"`
	LWarn     bool                          `json:"lwarn"`
	Ran       bool                          `json:"ran"`
	CheckRuns int                           `json:"checkRuns"`
	Cache     [2]string                     `json:"cache"`
	Approvals map[string]approveRefObserved `json:"approvals"`
}

type approveRefObserved struct {
	N      int    `json:"n"`
	SHA    string `json:"sha"`
	By     string `json:"by"`
	Commit string `json:"commit"`
	Base   string `json:"base"`
	Feas   string `json:"feas"`
}

func (s *approveSlice) Reset(ctx context.Context) error {
	if err := resetWorld(ctx, &s.world, true); err != nil {
		return err
	}
	s.last, s.exit, s.sha8, s.approver, s.feasibility = "ok", 0, "", "", ""
	s.bwarn, s.lwarn, s.ran = false, false, false
	s.lastCache = [2]string{"", ""}
	s.digestSymbols = map[string]string{}
	s.commitSymbols = map[contract.ObjectID]string{}
	s.baseSymbols = map[contract.ObjectID]string{}
	return nil
}

func (s *approveSlice) HasEventTag(tag string) bool { return tag == "Approve" || tag == "Init" }

func (s *approveSlice) Apply(ctx context.Context, event protocol.Event) error {
	if ctx == nil {
		return errors.New("intentapprove: context is required")
	}
	if event.Tag == "Init" {
		return s.Reset(ctx)
	}
	if event.Tag != "Approve" {
		return fmt.Errorf("intentapprove: unknown approval event %q", event.Tag)
	}
	value, err := decodeValue(event)
	if err != nil {
		return err
	}
	s.applyErr = nil
	s.applyEvent(ctx, value)
	return s.applyErr
}

func (s *approveSlice) Observe(ctx context.Context) (json.RawMessage, error) {
	observation := approveObservation{
		Last: s.last, Exit: s.exit, SHA8: s.sha8, Approver: s.approver,
		Feas: s.feasibility, BWarn: s.bwarn, LWarn: s.lwarn, Ran: s.ran,
		CheckRuns: s.world.checks.baselineCount(), Cache: s.lastCache,
		Approvals: map[string]approveRefObserved{},
	}
	for _, slug := range []string{"alpha", "bravo"} {
		approval, err := readApprovalPackage(ctx, s.world, slug)
		if err != nil {
			return nil, err
		}
		if approval == nil {
			continue
		}
		sha := approval.Hash
		if symbol := s.digestSymbols[approval.Hash]; symbol != "" {
			sha = symbol
		}
		commit := string(approval.Commit)
		if symbol := s.commitSymbols[approval.Commit]; symbol != "" {
			commit = symbol
		}
		base := approval.Base
		if id, err := gitio.ParseObjectID(base); err == nil {
			if symbol := s.baseSymbols[id]; symbol != "" {
				base = symbol
			}
		}
		feasibility := approval.Feas
		if feasibility == "" {
			feasibility = "not checked"
		}
		observation.Approvals[slug] = approveRefObserved{
			N: approval.Count, SHA: sha, By: approval.By, Commit: commit,
			Base: base, Feas: feasibility,
		}
	}
	return marshalObservation(observation)
}

func (s *approveSlice) Close() error { return s.world.close() }

func (s *approveSlice) applyEvent(ctx context.Context, value eventValue) {
	s.last, s.exit, s.sha8, s.approver, s.feasibility = "", 0, "", "", ""
	s.bwarn, s.lwarn, s.ran = false, false, false
	slug, err := value.text("slug")
	if err != nil {
		s.fail("usage", 2)
		return
	}
	if !knownSlug(slug) {
		s.fail("intent/not_found", 2)
		return
	}
	baseTreeSymbol, err := value.text("baseTree")
	if err != nil {
		baseTreeSymbol = "tree-a"
	}
	baseTree, err := s.world.ensureBaseSymbol(ctx, baseTreeSymbol)
	if err != nil {
		s.fail("environment/setup_failed", 3)
		return
	}
	setupStatus, _ := value.optionalText("setup", "ok")
	baselineStatus, _ := value.optionalText("baseline", "green")
	acceptanceStatus, _ := value.optionalText("acceptance", "green")
	cacheSymbol, _ := value.optionalText("cacheKey", "default")
	if cacheSymbol == "" {
		cacheSymbol = "default"
	}
	by, _ := value.optionalText("by", "")
	ident, _ := value.optionalText("ident", "Ann <ann@x.io>")
	byBad, _ := value.boolean("byBad", false)
	witnessMode, err := value.boolean("witnessMode", false)
	if err != nil {
		s.fail("usage", 2)
		return
	}
	if byBad && by == "" {
		by = "\n"
	}
	if err := s.world.applyIdentity(ctx, ident); err != nil {
		s.fail("environment/approval_identity_unavailable", 2)
		return
	}
	intentBytes, err := s.fixtureIntentBytes(value)
	if err != nil {
		s.fail("usage", 2)
		return
	}
	acceptanceBytes, err := value.optionalBytes("acceptanceBytes")
	if err != nil {
		s.fail("usage", 2)
		return
	}
	if acceptanceBytes == nil {
		acceptanceBytes = validAcceptance
	}
	if err := s.world.writeSources(slug, intentBytes, acceptanceBytes); err != nil {
		s.fail("environment/approval_check_failed", 3)
		return
	}
	missing, _ := value.boolean("missing", false)
	if missing {
		root, rootErr := safefs.OpenRoot(s.world.checkout)
		if rootErr != nil {
			s.fail("environment/approval_check_failed", 3)
			return
		}
		rootErr = root.Remove(fmt.Sprintf(acceptancePathFormat, slug))
		_ = root.Close()
		if rootErr != nil {
			s.fail("environment/approval_check_failed", 3)
			return
		}
		acceptanceBytes = nil
	}
	if err := s.world.setControls(
		controlStatus(setupStatus), controlStatus(baselineStatus), controlStatus(acceptanceStatus),
	); err != nil {
		s.fail("environment/approval_check_failed", 3)
		return
	}
	intentLint, parseError, lintError, lintWarning := inspectIntent(slug, intentBytes)
	s.lwarn = lintWarning
	_ = parseError
	_ = lintError
	actualHash := intent.ApprovalSHA256(intentBytes, acceptanceBytes)
	shaSymbol, _ := value.optionalText("sha", "")
	sha8Symbol, _ := value.optionalText("sha8", "")
	sha8 := symbolPrefix(actualHash, shaSymbol, sha8Symbol)
	given, _ := value.optionalText("given", "")
	actualClaim, matches, err := approvalClaim(actualHash, given, shaSymbol)
	if err != nil {
		s.fail("intent/hash_mismatch", 1)
		return
	}
	if given != "" && matches && shaSymbol != "" {
		s.digestSymbols[actualHash] = shaSymbol
	}
	if by != "" && strings.ContainsAny(by, "\r\n") {
		// The production preparer validates the value below; this keeps all
		// outcome selection on its real command path.
	}
	if err := s.world.setControls(controlStatus(setupStatus), controlStatus(baselineStatus), controlStatus(acceptanceStatus)); err != nil {
		s.fail("environment/approval_check_failed", 3)
		return
	}
	deps := s.world.prepareDependencies()
	observer := deps.Baselines.(*baselineObserver)
	request := prepare.Request{
		Project: s.world.project, Slug: slug, HashPrefix: actualClaim, By: by,
		RunDir: s.world.runDir, BaseEnvironment: cloneProcessEnvironment(s.world.env),
		Toolchain: map[string]string{"fixture": "intentapprove-v1"}, ToolchainKnown: true,
		AdapterVersion: "intentapprove-" + cacheSymbol,
	}
	if err := s.world.clearMiseProbeLog(); err != nil {
		s.fail("environment/approval_check_failed", 3)
		return
	}
	prepared, prepareErr := prepare.Prepare(ctx, request, deps)
	s.ran = observer.ComputeRan()
	if observer.GetCalled() {
		if observer.Key().CheckedBaseTree != baseTree {
			s.fail("environment/approval_check_failed", 3)
			return
		}
		s.lastCache = [2]string{baseTreeSymbol, cacheSymbol}
		s.bwarn = baselineIsRed(observer.rows)
	}
	if prepareErr != nil {
		code, exit := prepareOutcome(prepareErr)
		s.fail(code, exit)
		if code == "intent/hash_mismatch" {
			s.sha8 = sha8
		}
		return
	}
	if prepared == nil {
		s.fail("environment/approval_check_failed", 3)
		return
	}
	if intentLint {
		s.lwarn = true
	}
	if observer.GetCalled() {
		s.bwarn = baselineIsRed(observer.rows)
	}
	if given == "" {
		s.last, s.exit, s.sha8 = "needs_decision", 5, sha8
		s.approver = prepared.Approver
		s.feasibility = "not checked"
		return
	}
	if prepared.ApprovalSHA256 != actualHash {
		s.fail("intent/hash_mismatch", 1)
		return
	}
	lateBytes, latePath, lateErr := lateWriteFor(value, slug, prepared)
	if lateErr != nil {
		s.applyErr = lateErr
		return
	}
	dependencies := publish.Dependencies{Git: deps.Git, Policy: deps.Policy, Roots: deps.Roots}
	if lateBytes != nil {
		dependencies.Roots = &lateWriteOpener{base: safefs.Opener{}, checkout: s.world.checkout, name: latePath, bytes: lateBytes}
	}
	if witnessMode {
		if lateBytes != nil && !lateWriteIdentical(prepared, latePath, lateBytes) {
			_, publishErr := publish.Publish(ctx, publish.Request{
				Project: s.world.project, Prepared: prepared, GivenHashPrefix: actualClaim, At: fixtureTimestamp,
			}, dependencies)
			if publishErr != nil {
				code, exit := publishOutcome(publishErr)
				s.fail(code, exit)
				if code == "intent/hash_mismatch" {
					s.setLateDigestSymbol(ctx, value, slug, sha8)
				}
				return
			}
			s.fail("environment/approval_publish_failed", 3)
			return
		}
		// No witness record is accepted from the model's `feas` text. The real
		// package needs a witness commit and a byte-derived diff digest.
		s.fail("intent/unproven", 1)
		return
	}
	result, publishErr := publish.Publish(ctx, publish.Request{
		Project: s.world.project, Prepared: prepared, GivenHashPrefix: actualClaim, At: fixtureTimestamp,
	}, dependencies)
	if publishErr != nil {
		code, exit := publishOutcome(publishErr)
		s.fail(code, exit)
		if code == "intent/hash_mismatch" {
			s.setLateDigestSymbol(ctx, value, slug, sha8)
		}
		return
	}
	if result == nil {
		s.fail("environment/approval_publish_failed", 3)
		return
	}
	s.digestSymbols[prepared.ApprovalSHA256] = shaSymbol
	s.commitSymbols[result.Commit] = valueString(value, "commit", "")
	s.baseSymbols[prepared.BaseCommit] = valueString(value, "baseSha", "")
	s.last, s.exit, s.sha8 = "ok", 0, sha8
	s.approver, s.feasibility = prepared.Approver, "not checked"
}

func lateWriteIdentical(prepared *prepare.Prepared, name string, data []byte) bool {
	if prepared == nil {
		return false
	}
	switch name {
	case ".kogen/intents/" + prepared.Intent.Slug + "/intent.md":
		return bytes.Equal(data, prepared.IntentBytes)
	case prepared.AcceptanceSourcePath:
		return bytes.Equal(data, prepared.AcceptanceBytes)
	default:
		return false
	}
}

func (s *approveSlice) setLateDigestSymbol(ctx context.Context, value eventValue, slug, fallback string) {
	if after, acceptance, exists, readErr := currentSources(ctx, s.world, slug); readErr == nil && exists {
		lateHash := intent.ApprovalSHA256(after, acceptance)
		newSHA8, _ := value.optionalText("newSha8", "")
		s.sha8 = sourceHashSymbol(lateHash, newSHA8)
		if newSHA8 != "" {
			s.digestSymbols[lateHash] = newSHA8
		}
	} else {
		s.sha8 = fallback
	}
}

func (s *approveSlice) fixtureIntentBytes(value eventValue) ([]byte, error) {
	if data, err := value.optionalBytes("intentBytes"); err != nil || data != nil {
		return data, err
	}
	if parseError, _ := value.boolean("parseErr", false); parseError {
		return []byte("---\ntitle: Broken\nsize: small\ndomains: [app]\n"), nil
	}
	if lintError, _ := value.boolean("lintErr", false); lintError {
		return bytesReplace(validIntent, []byte("- A1: test"), []byte("- A1: example")), nil
	}
	if lintWarning, _ := value.boolean("lintWarn", false); lintWarning {
		return bytesReplace(validIntent, []byte("Update the greeting output."), []byte("Maybe it might be possible to update the greeting output.")), nil
	}
	return append([]byte(nil), validIntent...), nil
}

func inspectIntent(slug string, source []byte) (lint bool, parseError bool, lintError bool, lintWarning bool) {
	parsed, err := intent.Parse(slug, source)
	if err != nil {
		return false, true, false, false
	}
	for _, issue := range parsed.Lint() {
		if issue.Severity == intent.LintError {
			lintError = true
		}
		if issue.Severity == intent.LintStyle {
			lintWarning = true
		}
	}
	return lintWarning, false, lintError, lintWarning
}

func controlStatus(status string) string {
	switch status {
	case "", "ok":
		return "green"
	case "failed", "red":
		return "red"
	case "tool_missing", "unavailable":
		return "unavailable"
	case "timeout":
		return "timeout"
	default:
		return status
	}
}

func baselineIsRed(rows []prepare.BaselineRow) bool {
	for _, row := range rows {
		if row.Status == contract.CheckRed {
			return true
		}
	}
	return false
}

func lateWriteFor(value eventValue, slug string, prepared *prepare.Prepared) ([]byte, string, error) {
	if raw, ok := value["lateIntentBytes"]; ok {
		data, err := rawBytes(raw)
		return data, fmt.Sprintf(intentPathFormat, slug), err
	}
	if raw, ok := value["lateAcceptanceBytes"]; ok {
		data, err := rawBytes(raw)
		return data, prepared.AcceptanceSourcePath, err
	}
	stable, err := value.boolean("stableBeforeCas", true)
	if err != nil {
		return nil, "", err
	}
	if stable {
		return nil, "", nil
	}
	return nil, "", errors.New("intentapprove: stableBeforeCas=false requires actual late source bytes")
}

func symbolPrefix(actual, digestSymbol, prefixSymbol string) string {
	if prefixSymbol != "" && digestSymbol != "" && strings.HasPrefix(digestSymbol, prefixSymbol) {
		return prefixSymbol
	}
	if prefixSymbol != "" && strings.HasPrefix(actual, prefixSymbol) {
		return prefixSymbol
	}
	if len(actual) >= 8 {
		return actual[:8]
	}
	return actual
}

// sourceHashSymbol attaches an input-event alias to a digest only after the
// adapter has hashed the corresponding bytes. A blank alias leaves the real
// digest prefix visible.
func sourceHashSymbol(actual, alias string) string {
	if alias != "" {
		return alias
	}
	if len(actual) >= 8 {
		return actual[:8]
	}
	return actual
}

func valueString(value eventValue, field, fallback string) string {
	text, err := value.optionalText(field, fallback)
	if err != nil {
		return fallback
	}
	return text
}

func (s *approveSlice) fail(code string, exit int) {
	s.last, s.exit = code, exit
	s.approver, s.feasibility = "", ""
	s.bwarn, s.lwarn = false, false
	if code != "intent/hash_mismatch" {
		s.sha8 = ""
	}
}

func bytesReplace(source, old, replacement []byte) []byte {
	return []byte(strings.Replace(string(source), string(old), string(replacement), 1))
}

func rawBytes(raw json.RawMessage) ([]byte, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []byte(text), nil
	}
	var values []byte
	if err := json.Unmarshal(raw, &values); err == nil {
		return values, nil
	}
	return nil, errors.New("intentapprove: byte source must be a string or byte array")
}

func (v eventValue) optionalBytes(name string) ([]byte, error) {
	raw, ok := v[name]
	if !ok {
		return nil, nil
	}
	return rawBytes(raw)
}

var _ protocol.Slice = (*approveSlice)(nil)
var _ = fs.ErrNotExist
