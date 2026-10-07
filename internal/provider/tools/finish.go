package tools

import (
	"encoding/json"
)

const emptyFinishFeedback = "Kogen found no changed files. Make the requested change before claiming done."

// FinishAction is the result of the implementation-change guard after a
// syntactically valid, standalone finish call.
type FinishAction uint8

const (
	FinishContinue FinishAction = iota + 1
	FinishVerify
)

// FinishDecision carries the controller feedback for a rejected first empty
// completion claim. A second empty claim proceeds to verification.
type FinishDecision struct {
	Action   FinishAction
	Feedback string
}

// FinishPolicy tracks empty completion claims for one builder conversation.
// implementationChanged excludes approved Intent and acceptance files.
type FinishPolicy struct {
	emptyFinishes uint8
}

func (p *FinishPolicy) EmptyFinishes() uint8 {
	if p == nil {
		return 0
	}
	return p.emptyFinishes
}

func (p *FinishPolicy) Finish(implementationChanged bool) FinishDecision {
	if implementationChanged {
		return FinishDecision{Action: FinishVerify}
	}
	if p == nil {
		return FinishDecision{Action: FinishContinue, Feedback: emptyFinishFeedback}
	}
	if p.emptyFinishes > 0 {
		return FinishDecision{Action: FinishVerify}
	}
	p.emptyFinishes = 1
	return FinishDecision{Action: FinishContinue, Feedback: emptyFinishFeedback}
}

func executeFinish(raw json.RawMessage, callCount int) (string, error) {
	object, err := decodeToolObject(raw)
	if err != nil || len(object) != 0 || callCount != 1 {
		return "", toolError(finishGuardText)
	}
	return completionRequested, nil
}
