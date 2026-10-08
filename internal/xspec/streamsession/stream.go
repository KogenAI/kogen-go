package streamsession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/optional/checkpoint"
	"kogen-go/internal/provider/retry"
	"kogen-go/internal/provider/session"
	"kogen-go/internal/provider/wire"
	"kogen-go/internal/xspec/protocol"
)

const (
	streamPhaseIdle    = "idle"
	streamPhaseOpen    = "open"
	streamPhaseStopped = "stopped"
	streamReasonPrefix = "provider/"
)

var errEffectPending = errors.New("replay has no provider result for the next attempt")

type streamObservation struct {
	Phase         string `json:"phase"`
	Mode          string `json:"mode"`
	Role          string `json:"role"`
	Model         string `json:"model"`
	FallbackOn    bool   `json:"fallbackOn"`
	Refreshable   bool   `json:"refreshable"`
	Bounded       bool   `json:"bounded"`
	Wall          uint64 `json:"wall"`
	Attempt       int    `json:"attempt"`
	Overloads     int    `json:"overloads"`
	Refreshed     bool   `json:"refreshed"`
	Waited        uint64 `json:"waited"`
	Decision      string `json:"decision"`
	Delay         uint64 `json:"delay"`
	Reason        string `json:"reason"`
	Continued     bool   `json:"continued"`
	Queued        bool   `json:"queued"`
	Exit          int    `json:"exit"`
	Checkpoint    string `json:"checkpoint"`
	Continuations uint32 `json:"continuations"`
	Last          string `json:"last"`
	Failed        bool   `json:"failed"`
}

type openInput struct {
	Role        string `json:"role"`
	Model       string `json:"model"`
	FallbackOn  bool   `json:"fallbackOn"`
	Refreshable bool   `json:"refreshable"`
	Bounded     bool   `json:"bounded"`
	Wall        int64  `json:"wall"`
	Mode        string `json:"mode"`
}

type resultInput struct {
	Kind  string `json:"kind"`
	Items bool   `json:"items"`
}

type checkpointInput struct {
	Kind string `json:"kind"`
}

type streamEffect struct {
	kind  string
	items bool
}

type streamSlice struct {
	obs             streamObservation
	opened          bool
	config          openInput
	effects         []streamEffect
	requestWaited   uint64
	continuations   uint32
	checkpointState *checkpoint.ContinuationTurn
}

func newStreamSlice() *streamSlice {
	return &streamSlice{obs: streamObservation{Phase: streamPhaseIdle, Refreshable: true, Last: "ok"}}
}

func (s *streamSlice) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	*s = *newStreamSlice()
	return nil
}

func (s *streamSlice) HasEventTag(tag string) bool {
	switch tag {
	case "Init", "Open", "Result", "Checkpoint", "SetWaited":
		return true
	default:
		return false
	}
}

func (s *streamSlice) Apply(ctx context.Context, event protocol.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch event.Tag {
	case "Init":
		return s.Reset(ctx)
	case "Open":
		return s.open(event)
	case "Result":
		return s.result(ctx, event)
	case "Checkpoint":
		return s.checkpoint(event)
	case "SetWaited":
		return s.setWaited(event)
	default:
		return fmt.Errorf("streamsession: unsupported stream event %q", event.Tag)
	}
}

func (s *streamSlice) Observe(ctx context.Context) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(s.obs)
}

func (s *streamSlice) open(event protocol.Event) error {
	var input openInput
	if !event.HasValue || json.Unmarshal(event.Value, &input) != nil ||
		input.Role == "" || input.Model == "" || input.Mode == "" || input.Wall < 0 {
		s.obs.Last = "bad_open"
		return nil
	}
	if s.obs.Phase == streamPhaseOpen || !knownStreamRole(input.Role) ||
		!knownStreamModel(input.Model) || (input.Mode != string(retry.ModeBuild) && input.Mode != string(retry.ModeShape)) {
		s.obs.Last = "bad_open"
		return nil
	}
	if uint64(input.Wall) > uint64(math.MaxInt64/int64(time.Millisecond)) {
		s.obs.Last = "bad_open"
		return nil
	}

	s.config = input
	if input.Model == "grok" {
		// Grok has no overload model switch. The policy function below also
		// checks provider capability; this normalized field is the effective
		// model fallback capability exposed by the replay model.
		input.FallbackOn = false
		s.config.FallbackOn = false
	}
	s.effects = nil
	s.requestWaited = s.obs.Waited
	s.checkpointState = nil
	s.opened = true
	s.obs.Mode = input.Mode
	s.obs.Role = input.Role
	s.obs.Model = input.Model
	s.obs.FallbackOn = input.FallbackOn
	s.obs.Refreshable = input.Refreshable
	s.obs.Bounded = input.Bounded
	s.obs.Wall = uint64(input.Wall)
	s.obs.Phase = streamPhaseOpen
	s.obs.Attempt = 1
	s.obs.Overloads = 0
	s.obs.Refreshed = false
	s.obs.Decision = ""
	s.obs.Delay = 0
	s.obs.Reason = ""
	s.obs.Continued = false
	s.obs.Queued = true
	s.obs.Exit = 0
	s.obs.Checkpoint = ""
	s.obs.Last = "ok"
	s.obs.Failed = false
	return nil
}

func (s *streamSlice) result(ctx context.Context, event protocol.Event) error {
	if s.obs.Phase != streamPhaseOpen || !s.opened {
		s.obs.Last = "not_open"
		return nil
	}
	var input resultInput
	if !event.HasValue || json.Unmarshal(event.Value, &input) != nil {
		s.obs.Last = "bad_result"
		return nil
	}
	_, valid := classifyResult(input.Kind, input.Items)
	if !valid {
		s.obs.Last = "bad_result"
		return nil
	}
	s.effects = append(s.effects, streamEffect{kind: input.Kind, items: input.Items})
	return s.evaluate(ctx)
}

func (s *streamSlice) evaluate(ctx context.Context) error {
	clock := newReplayClock()
	var budget *retry.WallBudget
	if s.config.Bounded {
		budget = retry.NewWallBudget(clock, time.Duration(s.config.Wall)*time.Millisecond)
	}
	role := contract.RoleName(s.config.Role)
	current := roleSettings(s.config.Model)
	var fallback *contract.RoleSettings
	if current.Provider == "chatgpt" {
		target := contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "medium"}
		fallback = &target
	}
	request := retry.Request{
		Provider: current.Provider,
		Model:    current.Model,
		Effort:   current.Effort,
		Body:     []byte(`{"input":[]}`),
	}
	continueAt := -1
	lastConsumed := -1
	forcedRefresh := false
	policyState := &retry.State{WaitedMS: s.requestWaited}
	policyConfig := retry.Config{
		Mode:        retry.Mode(s.config.Mode),
		Role:        role,
		Current:     current,
		Fallback:    fallback,
		FallbackOn:  s.config.FallbackOn,
		Refreshable: s.config.Refreshable,
		Budget:      budget,
		Clock:       clock,
		Jitter:      ceilingJitter{},
		Hooks: retry.Hooks{
			Continue: func(_ context.Context, req retry.Request, items []json.RawMessage, instruction string) (retry.Request, error) {
				req.History = append(req.History, items...)
				controller, err := json.Marshal(map[string]any{
					"role": "user", "content": []any{map[string]string{"type": "input_text", "text": instruction}},
				})
				if err != nil {
					return retry.Request{}, err
				}
				req.History = append(req.History, controller)
				continueAt = lastConsumed
				return req, nil
			},
			Switch: func(_ context.Context, req retry.Request, target contract.RoleSettings) (retry.Request, error) {
				req.History = retry.DropEncryptedReasoning(req.History)
				req.Provider, req.Model, req.Effort = target.Provider, target.Model, target.Effort
				return req, nil
			},
		},
	}
	result, err := retry.Run(ctx, request, policyConfig, policyState, func(_ context.Context, req retry.Request, mode retry.AttemptMode) (retry.Outcome, error) {
		forcedRefresh = forcedRefresh || mode.ForceRefresh
		index := lastConsumed + 1
		if index >= len(s.effects) {
			return retry.Outcome{}, errEffectPending
		}
		lastConsumed = index
		effect := s.effects[index]
		class, valid := classifyResult(effect.kind, effect.items)
		if !valid {
			return retry.Outcome{}, errors.New("streamsession: stored effect became invalid")
		}
		switch class {
		case retry.Class("ok"):
			return retry.Outcome{Value: "completed"}, nil
		case retry.ClassIncomplete:
			return retry.Outcome{}, &retry.Failure{Class: class, Message: "scripted incomplete response"}
		default:
			failure := &retry.Failure{Class: class, Message: "scripted provider effect"}
			if effect.items {
				failure.PartialItems = []json.RawMessage{json.RawMessage(`{"type":"message","content":[]}`)}
			}
			return retry.Outcome{}, failure
		}
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && !errors.Is(err, errEffectPending) {
		var failure *retry.Failure
		if !errors.As(err, &failure) {
			return err
		}
	}

	s.obs.Refreshed = forcedRefresh
	s.obs.Attempt = 1
	for _, ev := range result.Events {
		if ev.Kind == "provider_retry" || ev.Kind == "provider_switch" {
			s.obs.Attempt++
		}
		if ev.Kind == "provider_switch" {
			s.obs.Model = "sol"
		}
	}
	s.obs.Overloads = overloadStreak(result.Events)
	s.obs.Continued = continueAt == len(s.effects)-1
	s.obs.Waited = policyState.WaitedMS
	s.obs.Delay = 0
	s.obs.Reason = ""
	s.obs.Exit = 0
	s.obs.Failed = false
	s.obs.Last = "ok"

	if err == nil {
		s.obs.Phase = streamPhaseIdle
		s.obs.Decision = "success"
		s.obs.Continued = false
		s.obs.Reason = ""
		return nil
	}
	var failure *retry.Failure
	if errors.As(err, &failure) && failure.Class == retry.ClassIncomplete {
		s.obs.Phase = streamPhaseIdle
		s.obs.Decision = "incomplete"
		s.obs.Continued = false
		return nil
	}
	if result.RestartStage {
		s.obs.Phase = streamPhaseIdle
		s.obs.Decision = "pause"
		s.obs.Continued = false
		s.obs.Reason = streamReasonPrefix + string(failure.Class)
		if len(result.Events) > 0 {
			s.obs.Delay = result.Events[len(result.Events)-1].DelayMS
		}
		return nil
	}
	if errors.Is(err, errEffectPending) {
		s.obs.Phase = streamPhaseOpen
		last := s.effects[len(s.effects)-1]
		lastClass, _ := classifyResult(last.kind, last.items)
		if lastClass == retry.ClassLogin && forcedRefresh {
			s.obs.Decision = "refresh"
			s.obs.Reason = "provider/login"
			s.obs.Continued = false
			return nil
		}
		if len(result.Events) == 0 {
			return errors.New("streamsession: production retry port returned a pending effect without a transition")
		}
		lastEvent := result.Events[len(result.Events)-1]
		s.obs.Decision = "retry"
		if lastEvent.Kind == "provider_switch" {
			s.obs.Decision = "switch"
			s.obs.Model = "sol"
		}
		s.obs.Delay = lastEvent.DelayMS
		s.obs.Reason = lastEvent.Reason
		s.obs.Continued = continueAt == len(s.effects)-1
		return nil
	}

	if failure == nil {
		return fmt.Errorf("streamsession: production retry returned an unclassified error: %w", err)
	}
	if failure.Class == retry.ClassLogin || failure.Class == retry.ClassUsageLimit ||
		failure.Class == retry.ClassTimeout || failure.Class == retry.ClassStall ||
		failure.Class == retry.ClassTransport || failure.Class == retry.ClassOverload ||
		failure.Class == retry.ClassMalformed {
		s.obs.Phase = streamPhaseStopped
		s.obs.Decision = "stop"
		s.obs.Delay = 0
		s.obs.Reason = streamReasonPrefix + string(failure.Class)
		s.obs.Continued = false
		s.obs.Exit = 4
		s.obs.Queued = true
		return nil
	}
	return fmt.Errorf("streamsession: unsupported production retry failure class %q", failure.Class)
}

func (s *streamSlice) checkpoint(event protocol.Event) error {
	if s.obs.Phase != streamPhaseOpen || !s.opened {
		s.obs.Last = "not_open"
		return nil
	}
	var input checkpointInput
	if !event.HasValue || json.Unmarshal(event.Value, &input) != nil {
		s.obs.Last = "bad_checkpoint"
		return nil
	}
	if input.Kind != "valid" && input.Kind != "invalid" && input.Kind != "oversized" {
		s.obs.Last = "bad_checkpoint"
		return nil
	}
	maxBytes := 1024
	summary := "accepted"
	if input.Kind == "invalid" {
		summary = " "
	}
	if input.Kind == "oversized" {
		summary = strings.Repeat("x", maxBytes*2)
	}
	checkpointValue, err := checkpoint.BuildCheckpoint(summary, maxBytes)
	if err != nil {
		s.obs.Phase = streamPhaseStopped
		s.obs.Decision = "stop"
		s.obs.Delay = 0
		s.obs.Reason = checkpoint.ContinuationFailedReason
		s.obs.Continued = false
		s.obs.Exit = 1
		s.obs.Checkpoint = "failed"
		s.obs.Queued = true
		s.obs.Last = "ok"
		return nil
	}
	// Use the production session/checkpoint boundary with isolated approved
	// fixture bytes. The Quint model intentionally observes only the accepted
	// continuation count, not the digest or input contents.
	identity, err := session.Bind(session.Binding{
		RunID: "run_xspec_stream", CacheKey: "cache_xspec_stream", Role: "builder",
		Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max",
		Stage: "develop", Attempt: "builder", Rung: "builder", Epoch: "initial",
	})
	if err != nil {
		return err
	}
	requestItem := json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"fixture request"}]}`)
	planItem := json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"fixture plan"}]}`)
	turn, err := checkpoint.NewContinuation(identity, checkpoint.ApprovedInputs{Request: requestItem, Plan: planItem}, checkpointValue, wire.DefaultPrefix())
	if err != nil {
		return err
	}
	s.checkpointState = &turn
	s.continuations++
	s.obs.Continuations = s.continuations
	s.obs.Phase = streamPhaseIdle
	s.obs.Decision = "checkpoint"
	s.obs.Delay = 0
	s.obs.Reason = ""
	s.obs.Continued = false
	s.obs.Exit = 0
	s.obs.Checkpoint = "accepted"
	s.obs.Last = "ok"
	return nil
}

func (s *streamSlice) setWaited(event protocol.Event) error {
	var input struct {
		MS int64 `json:"ms"`
	}
	if !event.HasValue || json.Unmarshal(event.Value, &input) != nil || input.MS < 0 {
		s.obs.Last = "bad_wait"
		return nil
	}
	waited := uint64(input.MS)
	if waited != 0 && waited != uint64(retry.PauseDuration/time.Millisecond) &&
		waited != 86_100_000 && waited != uint64(retry.PauseLimit/time.Millisecond) {
		s.obs.Last = "bad_wait"
		return nil
	}
	s.obs.Waited = waited
	if s.obs.Phase == streamPhaseOpen {
		s.requestWaited = waited
	}
	s.obs.Last = "ok"
	return nil
}

func knownStreamRole(role string) bool { return role == "builder" || role == "planner" }

func knownStreamModel(model string) bool { return model == "luna" || model == "sol" || model == "grok" }

func roleSettings(model string) contract.RoleSettings {
	switch model {
	case "sol":
		return contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "medium"}
	case "grok":
		return contract.RoleSettings{Provider: "grok", Model: "grok-4.6", Effort: "high"}
	default:
		return contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max"}
	}
}

func classifyResult(kind string, items bool) (retry.Class, bool) {
	switch kind {
	case "ok":
		return retry.Class("ok"), true
	case "first_byte", "total":
		return retry.ClassTimeout, true
	case "cut":
		if items {
			return retry.ClassMalformed, true
		}
		return retry.ClassTransport, true
	case "overload":
		return retry.ClassOverload, true
	case "malformed":
		return retry.ClassMalformed, true
	case "transport":
		return retry.ClassTransport, true
	case "timeout":
		return retry.ClassTimeout, true
	case "stall":
		return retry.ClassStall, true
	case "usage_limit":
		return retry.ClassUsageLimit, true
	case "login":
		return retry.ClassLogin, true
	case "incomplete":
		return retry.ClassIncomplete, true
	default:
		return "", false
	}
}

func overloadStreak(events []retry.Event) int {
	streak := 0
	for _, event := range events {
		switch event.Kind {
		case "provider_switch":
			streak = 0
		case "provider_retry":
			if event.Reason == "provider/overload" {
				streak++
			} else {
				streak = 0
			}
		case "provider_wait":
			// Credential waits do not reset the production overload streak.
		}
	}
	return streak
}
