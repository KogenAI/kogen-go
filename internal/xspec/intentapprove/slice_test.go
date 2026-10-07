package intentapprove

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"kogen-go/internal/gitio"
	"kogen-go/internal/journal"
	"kogen-go/internal/safefs"
	"kogen-go/internal/xspec/protocol"
)

func TestHashClaimsUseByteDerivedDigestAndIgnorePrefixOK(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	claim, matched, err := intentClaim(digest, "abcd1234")
	if err != nil || !matched || claim != digest[:8] {
		t.Fatalf("intent claim = %q, %t, %v", claim, matched, err)
	}
	claim, matched, err = intentClaim(digest, "ffff0000")
	if err != nil || matched || strings.HasPrefix(digest, claim) {
		t.Fatalf("mismatch claim = %q, %t, %v", claim, matched, err)
	}

	claimA, matchedA, errA := approvalClaim(digest, "abcd1234", "abcd1234bbbb2222")
	claimB, matchedB, errB := approvalClaim(digest, "abcd1234", "abcd1234bbbb2222")
	if errA != nil || errB != nil || !matchedA || !matchedB || claimA != claimB {
		t.Fatalf("claim mapping changed with unrelated prefixOk input: (%q,%t,%v) / (%q,%t,%v)", claimA, matchedA, errA, claimB, matchedB, errB)
	}
}

func TestLateHashClaimRequiresActualSecondReadBytes(t *testing.T) {
	value := eventValue{"stableBeforeCas": json.RawMessage(`false`)}
	if _, _, err := lateWriteFor(value, "alpha", nil); err == nil {
		t.Fatal("stableBeforeCas=false was converted into synthetic source bytes")
	}
	if validateByClaim("", true) == nil {
		t.Fatal("byBad=true was converted into a synthetic invalid argument")
	}
	if err := validateByClaim("bad\nname", true); err != nil {
		t.Fatalf("actual multiline --by value was rejected: %v", err)
	}
}

func TestIntentSlicePublishesRealApprovalAndRetriesRealCAS(t *testing.T) {
	ctx := context.Background()
	adapter, err := IntentFactory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slice := adapter.(*intentSlice)
	t.Cleanup(func() { _ = slice.Close() })
	if !slice.HasEventTag("Init") || slice.Apply(ctx, protocol.Event{Tag: "Init"}) != nil {
		t.Fatal("intent Init event was not reset through the production fixture")
	}

	applyEventJSON(t, ctx, slice, "Shape", `{"slug":"alpha","result":"valid"}`)
	applyEventJSON(t, ctx, slice, "Approve", `{"slug":"alpha","mode":"commit","hash":"abcd1234","prefixOk":false,"race":"once"}`)

	var observed intentObservation
	decodeObservation(t, ctx, slice, &observed)
	if observed.Last != "ok" || observed.Did != "approved" || observed.CASTries != 2 {
		t.Fatalf("approval observation = %+v", observed)
	}
	packageData, err := readApprovalPackage(ctx, slice.world, "alpha")
	if err != nil || packageData == nil || packageData.Count != 1 {
		t.Fatalf("real approval package = %+v, %v", packageData, err)
	}
	if _, err := gitio.ParseObjectID(string(packageData.Target)); err != nil {
		t.Fatalf("approval ref target is not an object ID: %v", err)
	}
	refs := gitio.NewRefPort(slice.world.git, slice.world.policy(slice.world.origin))
	ref, err := refs.ReadRef(ctx, packageData.Ref)
	if err != nil || !ref.Exists || ref.Target != packageData.Target {
		t.Fatalf("published ref = %+v, %v", ref, err)
	}
}

func TestIntentSliceRetainsDanglingRefAfterTwoRealCASLosses(t *testing.T) {
	ctx := context.Background()
	adapter, err := IntentFactory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slice := adapter.(*intentSlice)
	t.Cleanup(func() { _ = slice.Close() })
	applyEventJSON(t, ctx, slice, "Shape", `{"slug":"bravo","result":"valid"}`)
	applyEventJSON(t, ctx, slice, "Approve", `{"slug":"bravo","mode":"commit","hash":"abcd1234","prefixOk":true,"race":"twice"}`)

	var observed intentObservation
	decodeObservation(t, ctx, slice, &observed)
	if observed.Last != "controller/approval_cas_lost" || observed.CASTries != 2 {
		t.Fatalf("CAS loss observation = %+v", observed)
	}
	refs := gitio.NewRefPort(slice.world.git, slice.world.policy(slice.world.origin))
	ref, err := refs.ReadRef(ctx, "refs/kogen/intents/bravo")
	if err != nil || !ref.Exists {
		t.Fatalf("competing ref was not preserved: %+v, %v", ref, err)
	}
	if observed.Refs["bravo"].N != 0 {
		t.Fatalf("dangling competitor was reported as a published approval: %+v", observed.Refs["bravo"])
	}
}

func TestIntentSliceRemoveUsesRealSourceAndRefEffects(t *testing.T) {
	ctx := context.Background()
	adapter, err := IntentFactory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slice := adapter.(*intentSlice)
	t.Cleanup(func() { _ = slice.Close() })
	applyEventJSON(t, ctx, slice, "Shape", `{"slug":"alpha","result":"valid"}`)
	applyEventJSON(t, ctx, slice, "Approve", `{"slug":"alpha","mode":"commit","hash":"abcd1234","race":"none"}`)
	applyEventJSON(t, ctx, slice, "Remove", `{"slug":"alpha","force":false}`)
	var refused intentObservation
	decodeObservation(t, ctx, slice, &refused)
	if refused.Last != "intent/remove_requires_force" || refused.Refs["alpha"].N != 1 {
		t.Fatalf("unforced remove changed approval state: %+v", refused)
	}

	applyEventJSON(t, ctx, slice, "Remove", `{"slug":"alpha","force":true}`)
	var removed intentObservation
	decodeObservation(t, ctx, slice, &removed)
	if removed.Last != "ok" || removed.Did != "removed" || removed.Refs["alpha"].N != 0 {
		t.Fatalf("forced remove observation = %+v", removed)
	}
	root, err := safefs.OpenRoot(slice.world.checkout)
	if err != nil {
		t.Fatal(err)
	}
	_, sourceErr := root.ReadFile(".kogen/intents/alpha/intent.md")
	_ = root.Close()
	if !errors.Is(sourceErr, fs.ErrNotExist) {
		t.Fatalf("removed source still exists (err=%v)", sourceErr)
	}
	refs := gitio.NewRefPort(slice.world.git, slice.world.policy(slice.world.origin))
	ref, err := refs.ReadRef(ctx, "refs/kogen/intents/alpha")
	if err != nil || ref.Exists {
		t.Fatalf("approval ref survived remove: %+v, %v", ref, err)
	}
}

func TestIntentSliceAdoptWritesProductionJournalAndClaim(t *testing.T) {
	ctx := context.Background()
	adapter, err := IntentFactory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slice := adapter.(*intentSlice)
	t.Cleanup(func() { _ = slice.Close() })
	applyEventJSON(t, ctx, slice, "Shape", `{"slug":"bravo","result":"valid"}`)
	applyEventJSON(t, ctx, slice, "Approve", `{"slug":"bravo","mode":"commit","hash":"abcd1234","race":"none"}`)
	applyEventJSON(t, ctx, slice, "Adopt", `{"slug":"bravo","status":"building"}`)
	var building intentObservation
	decodeObservation(t, ctx, slice, &building)
	if building.Life["bravo"] != "building" || building.Last != "ok" {
		t.Fatalf("building adoption = %+v", building)
	}
	runID := runIDForSlug("bravo")
	stateRoot, err := safefs.OpenRoot(slice.world.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.NewRunStore(stateRoot, "runs/"+runID)
	if err != nil {
		_ = stateRoot.Close()
		t.Fatal(err)
	}
	snapshot, err := store.ReadSnapshot()
	_ = stateRoot.Close()
	approval, approvalErr := readApprovalPackage(ctx, slice.world, "bravo")
	if err != nil || approvalErr != nil || approval == nil || snapshot.Status != "running" || snapshot.ApprovalSHA256 != approval.Hash || snapshot.ApprovalCommit != string(approval.Target) {
		t.Fatalf("adopted production run snapshot = %+v, %v", snapshot, err)
	}
	refs := gitio.NewRefPort(slice.world.git, slice.world.policy(slice.world.origin))
	claim, err := refs.ReadRef(ctx, "refs/kogen/claim")
	if err != nil || !claim.Exists {
		t.Fatalf("active run claim is missing: %+v, %v", claim, err)
	}
	applyEventJSON(t, ctx, slice, "Remove", `{"slug":"bravo","force":true}`)
	var blocked intentObservation
	decodeObservation(t, ctx, slice, &blocked)
	if blocked.Last != "intent/remove_blocked" {
		t.Fatalf("active run did not block removal: %+v", blocked)
	}
	applyEventJSON(t, ctx, slice, "Adopt", `{"slug":"bravo","status":"failed"}`)
	claim, err = refs.ReadRef(ctx, "refs/kogen/claim")
	if err != nil || claim.Exists {
		t.Fatalf("terminal run retained its active claim: %+v, %v", claim, err)
	}
}

func TestApproveSliceUsesRealChecksCacheAndLateByteRecheck(t *testing.T) {
	ctx := context.Background()
	adapter, err := ApproveFactory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slice := adapter.(*approveSlice)
	t.Cleanup(func() { _ = slice.Close() })

	card := `{"slug":"alpha","given":"","sha":"abcd1234bbbb2222","sha8":"abcd1234","by":"ci-bot","ident":"Ann <ann@x.io>","baseTree":"tree-a","cacheKey":"k1","baseline":"green","acceptance":"green"}`
	applyEventJSON(t, ctx, slice, "Approve", card)
	var first approveObservation
	decodeObservation(t, ctx, slice, &first)
	if first.Last != "needs_decision" || first.CheckRuns != 1 || !first.Ran || first.Cache != [2]string{"tree-a", "k1"} {
		t.Fatalf("first card/check observation = %+v", first)
	}

	cardWarm := strings.Replace(card, `"baseline":"green"`, `"baseline":"red"`, 1)
	applyEventJSON(t, ctx, slice, "Approve", cardWarm)
	var warm approveObservation
	decodeObservation(t, ctx, slice, &warm)
	if warm.CheckRuns != 1 || warm.Ran || warm.BWarn {
		t.Fatalf("same-key check cache was not reused: %+v", warm)
	}
	cardRed := strings.Replace(cardWarm, `"cacheKey":"k1"`, `"cacheKey":"k2"`, 1)
	applyEventJSON(t, ctx, slice, "Approve", cardRed)
	var red approveObservation
	decodeObservation(t, ctx, slice, &red)
	if red.CheckRuns != 2 || !red.Ran || !red.BWarn {
		t.Fatalf("new-key red baseline was not measured: %+v", red)
	}
	witness := `{"slug":"alpha","given":"abcd1234","sha":"abcd1234bbbb2222","sha8":"abcd1234","by":"ci-bot","ident":"Ann <ann@x.io>","baseTree":"tree-a","cacheKey":"k1","baseline":"green","acceptance":"green","witnessMode":true,"feas":"PROVEN"}`
	applyEventJSON(t, ctx, slice, "Approve", witness)
	var unproven approveObservation
	decodeObservation(t, ctx, slice, &unproven)
	if unproven.Last != "intent/unproven" || unproven.CheckRuns != 2 || unproven.Ran || unproven.Approvals["alpha"].N != 0 {
		t.Fatalf("witness mode accepted a model claim or skipped measured checks: %+v", unproven)
	}

	late := `{"slug":"alpha","given":"abcd1234","prefixOk":false,"sha":"abcd1234bbbb2222","sha8":"abcd1234","newSha8":"bbbb2222","by":"ci-bot","ident":"Ann <ann@x.io>","baseTree":"tree-a","cacheKey":"k1","baseline":"green","acceptance":"green","lateAcceptanceBytes":"#!/bin/sh\nexit 9\n"}`
	applyEventJSON(t, ctx, slice, "Approve", late)
	var changed approveObservation
	decodeObservation(t, ctx, slice, &changed)
	if changed.Last != "intent/hash_mismatch" || changed.CheckRuns != 2 || changed.Ran || changed.SHA8 != "bbbb2222" {
		t.Fatalf("late-write result = %+v", changed)
	}
	if changed.Approvals["alpha"].N != 0 {
		t.Fatalf("late write published an approval: %+v", changed.Approvals)
	}
}

func applyEventJSON(t *testing.T, ctx context.Context, slice protocol.Slice, tag, value string) {
	t.Helper()
	if err := slice.Apply(ctx, protocol.Event{Tag: tag, HasValue: true, Value: json.RawMessage(value)}); err != nil {
		t.Fatalf("apply %s: %v", tag, err)
	}
}

func decodeObservation(t *testing.T, ctx context.Context, slice protocol.Slice, target any) {
	t.Helper()
	data, err := slice.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode observation %s: %v", data, err)
	}
}
