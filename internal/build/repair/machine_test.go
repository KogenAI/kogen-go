package repair

import (
	"strconv"
	"testing"

	"kogen-go/internal/contract"
)

func TestSixRepairsRequireStrictlyDecreasingRedCounts(t *testing.T) {
	state, effects, err := Step(NewState(), Event{Kind: EventDevelopmentFinished, Completion: CompletionFinished})
	if err != nil || len(effects) != 1 || effects[0].Kind != EffectVerify {
		t.Fatalf("initial development = state %#v, effects %#v, err %v", state, effects, err)
	}
	for count := 7; count >= 1; count-- {
		state, effects, err = Step(state, Event{Kind: EventVerification, Red: true, RedCount: count, HasRedCount: true, Feedback: "gate feedback"})
		if err != nil {
			t.Fatalf("verification count %d: %v", count, err)
		}
		if count > 1 {
			want := 8 - count
			if state.Phase != PhaseRepair || state.RepairsUsed != want || len(effects) != 1 || effects[0].Kind != EffectRunRepair || effects[0].RepairNumber != want {
				t.Fatalf("count %d did not request repair %d: state %#v effects %#v", count, want, state, effects)
			}
			if effects[0].Message != ControllerFeedback("gate feedback") {
				t.Fatalf("repair message = %q", effects[0].Message)
			}
			state, effects, err = Step(state, Event{Kind: EventRepairFinished, Completion: CompletionFinished, Changed: true})
			if err != nil || state.Phase != PhaseVerify || len(effects) != 1 || effects[0].Kind != EffectVerify {
				t.Fatalf("repair %d completion = state %#v effects %#v err %v", want, state, effects, err)
			}
		} else if state.Phase != PhaseDone || state.Reason != ReasonRepairCap || state.RepairsUsed != MaxRepairs || len(effects) != 0 {
			t.Fatalf("sixth repair did not end at cap: state %#v effects %#v", state, effects)
		}
	}
}

func TestEqualOrIncreasingRedCountEndsWithoutAnotherRepair(t *testing.T) {
	for _, next := range []int{5, 6} {
		t.Run(strconv.Itoa(next), func(t *testing.T) {
			state := startVerification(t)
			state, _, err := Step(state, Event{Kind: EventVerification, Red: true, RedCount: 5, HasRedCount: true})
			if err != nil {
				t.Fatal(err)
			}
			state, _, err = Step(state, Event{Kind: EventRepairFinished, Completion: CompletionFinished, Changed: true})
			if err != nil {
				t.Fatal(err)
			}
			state, effects, err := Step(state, Event{Kind: EventVerification, Red: true, RedCount: next, HasRedCount: true})
			if err != nil || state.Phase != PhaseDone || state.Reason != ReasonNoProgress || state.RepairsUsed != 1 || len(effects) != 0 {
				t.Fatalf("next count %d = state %#v effects %#v err %v", next, state, effects, err)
			}
		})
	}
}

func TestSecondUncountedRedVerificationEndsNoProgress(t *testing.T) {
	state := startVerification(t)
	state, effects, err := Step(state, Event{Kind: EventVerification, Red: true, Feedback: "first"})
	if err != nil || state.Phase != PhaseRepair || state.UncountedRedVerifications != 1 || effects[0].Kind != EffectRunRepair {
		t.Fatalf("first uncounted red = state %#v effects %#v err %v", state, effects, err)
	}
	state, _, err = Step(state, Event{Kind: EventRepairFinished, Completion: CompletionFinished, Changed: true})
	if err != nil {
		t.Fatal(err)
	}
	state, effects, err = Step(state, Event{Kind: EventVerification, Red: true, Feedback: "second"})
	if err != nil || state.Phase != PhaseDone || state.Reason != ReasonNoProgress || state.RepairsUsed != 1 || len(effects) != 0 {
		t.Fatalf("second uncounted red = state %#v effects %#v err %v", state, effects, err)
	}
}

func TestUnchangedRepairStillVerifiesThenEnds(t *testing.T) {
	state := startVerification(t)
	state, _, err := Step(state, Event{Kind: EventVerification, Red: true, RedCount: 2, HasRedCount: true})
	if err != nil {
		t.Fatal(err)
	}
	state, effects, err := Step(state, Event{Kind: EventRepairFinished, Completion: CompletionFinished, Changed: false})
	if err != nil || state.Phase != PhaseVerify || state.PendingReason != ReasonUnchanged || len(effects) != 1 || effects[0].Kind != EffectVerify {
		t.Fatalf("unchanged repair = state %#v effects %#v err %v", state, effects, err)
	}
	state, effects, err = Step(state, Event{Kind: EventVerification, Red: true, RedCount: 1, HasRedCount: true})
	if err != nil || state.Phase != PhaseDone || state.Reason != ReasonUnchanged || len(effects) != 0 {
		t.Fatalf("unchanged terminal verification = state %#v effects %#v err %v", state, effects, err)
	}
}

func TestTurnAndWallCapsRequestFinalVerification(t *testing.T) {
	for _, cap := range []Completion{CompletionTurnCap, CompletionWallCap} {
		t.Run(string(cap), func(t *testing.T) {
			state, effects, err := Step(NewState(), Event{Kind: EventDevelopmentFinished, Completion: cap})
			if err != nil || state.Phase != PhaseVerify || len(effects) != 1 || effects[0].Kind != EffectVerify {
				t.Fatalf("cap did not request final verification: state %#v effects %#v err %v", state, effects, err)
			}
			state, effects, err = Step(state, Event{Kind: EventVerification, Red: true, RedCount: 1, HasRedCount: true})
			want := ReasonTurnCap
			if cap == CompletionWallCap {
				want = ReasonWallCap
			}
			if err != nil || state.Phase != PhaseDone || state.Reason != want || len(effects) != 0 {
				t.Fatalf("cap verification = state %#v effects %#v err %v", state, effects, err)
			}
		})
	}
}

func TestFourthProtectedRestoreAppendsNotesStopsAndVerifies(t *testing.T) {
	state := NewState()
	for hit := 1; hit < ProtectedRestoreLimit; hit++ {
		var effects []Effect
		state, effects, _ = Step(state, Event{Kind: EventProtectedRestored, RestoredPaths: []string{"test/acceptance.t"}})
		if len(effects) != 1 || effects[0].Kind != EffectAppendControllerMessages || effects[0].Messages[0] != "You changed test/acceptance.t; acceptance tests and the Intent are read-only and have been restored. Make the implementation satisfy them." {
			t.Fatalf("restore %d effects = %#v", hit, effects)
		}
	}
	state, effects, err := Step(state, Event{Kind: EventProtectedRestored, RestoredPaths: []string{".kogen/intents/demo/intent.md"}})
	if err != nil || state.Phase != PhaseVerify || state.ProtectedRestores != 4 || state.PendingReason != ReasonProtectedRestoreLimit || len(effects) != 3 || effects[0].Kind != EffectAppendControllerMessages || effects[1].Kind != EffectStopDevelopment || effects[2].Kind != EffectVerify {
		t.Fatalf("fourth restore = state %#v effects %#v err %v", state, effects, err)
	}
	state, effects, err = Step(state, Event{Kind: EventVerification, Red: true, RedCount: 1, HasRedCount: true})
	if err != nil || state.Reason != ReasonProtectedRestoreLimit || state.Phase != PhaseDone || len(effects) != 0 {
		t.Fatalf("protected restore terminal verification = state %#v effects %#v err %v", state, effects, err)
	}
}

func TestCountRedUsesDistinctBlockingEvidence(t *testing.T) {
	identity := contract.FindingIdentity{Path: "a.go", Rule: "lint/x", Symbol: "f"}
	got := CountRed(RedInput{
		Checks: []RedCheck{
			{Red: true, Findings: []contract.FindingIdentity{identity}},
			{Red: true, Findings: []contract.FindingIdentity{identity}},
			{Red: true},
			{Red: false, Findings: []contract.FindingIdentity{{Path: "ignored", Rule: "x"}}},
		},
		FailedFixes: 1, FailedItems: 2,
	})
	if got != 5 {
		t.Fatalf("red count = %d, want 5", got)
	}
}

func startVerification(t *testing.T) State {
	t.Helper()
	state, effects, err := Step(NewState(), Event{Kind: EventDevelopmentFinished, Completion: CompletionFinished})
	if err != nil || state.Phase != PhaseVerify || len(effects) != 1 || effects[0].Kind != EffectVerify {
		t.Fatalf("start verification = state %#v effects %#v err %v", state, effects, err)
	}
	return state
}
