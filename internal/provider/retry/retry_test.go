package retry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"kogen-go/internal/contract"
)

type testClock struct {
	now   time.Time
	waits []time.Duration
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) Sleep(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.waits = append(c.waits, delay)
	c.now = c.now.Add(delay)
	return nil
}

func (c *testClock) advance(delay time.Duration) { c.now = c.now.Add(delay) }

type fixedJitter struct {
	draws []uint64
}

func (j *fixedJitter) Uint64n(upper uint64) (uint64, error) {
	if upper == 0 {
		return 0, errors.New("empty jitter range")
	}
	if len(j.draws) == 0 {
		return 0, nil
	}
	draw := j.draws[0]
	j.draws = j.draws[1:]
	return draw % upper, nil
}

func baseConfig(mode Mode) Config {
	return Config{
		Mode:        mode,
		Role:        contract.RoleName("builder"),
		Current:     contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-luna", Effort: "medium"},
		Clock:       newTestClock(),
		Jitter:      &fixedJitter{},
		Refreshable: false,
	}
}

func TestUnboundedTransientRequestStopsAfterFourAttempts(t *testing.T) {
	cfg := baseConfig(ModeShape)
	calls := 0
	result, err := Run(context.Background(), Request{Provider: "chatgpt", Body: []byte("same")}, cfg, nil, func(_ context.Context, _ Request, _ AttemptMode) (Outcome, error) {
		calls++
		return Outcome{}, &Failure{Class: ClassMalformed}
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Class != ClassMalformed {
		t.Fatalf("expected final malformed provider error, got %T %v", err, err)
	}
	if calls != MaxAttempts || result.Attempts != MaxAttempts {
		t.Fatalf("malformed attempt cap calls/result = %d/%d", calls, result.Attempts)
	}
	var delays []uint64
	for _, event := range result.Events {
		if event.Kind == "provider_retry" {
			delays = append(delays, event.DelayMS)
		}
	}
	if !reflect.DeepEqual(delays, []uint64{1000, 2000, 4000}) {
		t.Fatalf("jittered backoff sequence = %v", delays)
	}
}

func TestBoundedTimeoutMayRetryPastFourAttemptsWhenBudgetPays(t *testing.T) {
	clock := newTestClock()
	cfg := baseConfig(ModeBuild)
	cfg.Clock = clock
	cfg.Budget = NewWallBudget(clock, 45*time.Second)
	calls := 0
	result, err := Run(context.Background(), Request{Provider: "chatgpt", Body: []byte("unchanged")}, cfg, nil, func(_ context.Context, _ Request, _ AttemptMode) (Outcome, error) {
		calls++
		clock.advance(2 * time.Second)
		if calls <= 5 {
			return Outcome{}, &Failure{Class: ClassTimeout}
		}
		return Outcome{Text: "complete"}, nil
	})
	if err != nil {
		t.Fatalf("bounded transient retries should continue while budget can pay: %v", err)
	}
	if calls != 6 || result.Attempts != 6 {
		t.Fatalf("bounded retries stopped at %d attempts", result.Attempts)
	}
	if remaining := cfg.Budget.Remaining(); remaining != 33*time.Second {
		t.Fatalf("backoff waits consumed Build budget: %s remains", remaining)
	}
}

func TestIdenticalRetryThenPartialContinuationAccumulatesRawItems(t *testing.T) {
	cfg := baseConfig(ModeShape)
	initial := Request{
		Provider: "chatgpt",
		Model:    "gpt-6.1-luna",
		Effort:   "medium",
		Body:     []byte("body-before"),
		Headers:  http.Header{"Authorization": []string{"private-token"}},
	}
	var sent [][]byte
	calls := 0
	cfg.Hooks.Continue = func(_ context.Context, previous Request, items []json.RawMessage, instruction string) (Request, error) {
		if !bytes.Equal(previous.Body, []byte("body-before")) {
			t.Fatalf("continuation started from changed body %q", previous.Body)
		}
		if len(items) != 1 || instruction != ContinuationInstruction {
			t.Fatalf("continuation input/instruction = %q / %q", items, instruction)
		}
		previous.Body = []byte("body-after-continuation")
		previous.History = append(previous.History, items...)
		previous.History = append(previous.History, json.RawMessage("{\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"continue\"}]}"))
		return previous, nil
	}
	result, err := Run(context.Background(), initial, cfg, nil, func(_ context.Context, request Request, _ AttemptMode) (Outcome, error) {
		calls++
		sent = append(sent, bytes.Clone(request.Body))
		switch calls {
		case 1:
			return Outcome{}, &Failure{Class: ClassTransport}
		case 2:
			return Outcome{}, &Failure{Class: ClassMalformed, PartialItems: []json.RawMessage{
				json.RawMessage("{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"partial\"}]}"),
			}}
		default:
			return Outcome{RawItems: []json.RawMessage{json.RawMessage("{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"final\"}]}")}, Text: "final"}, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || !bytes.Equal(sent[0], sent[1]) || !bytes.Equal(sent[2], []byte("body-after-continuation")) {
		t.Fatalf("request bodies across retry/continuation = %q", sent)
	}
	if result.Outcome.Text != "partialfinal" || result.TextPrefix != "partial" || !result.Resumed {
		t.Fatalf("continued output not accumulated: %#v", result)
	}
	if len(result.RawItems) != 2 || string(result.RawItems[0]) != "{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"partial\"}]}" {
		t.Fatalf("continued raw history = %q", result.RawItems)
	}
	if result.Events[0].Resumed || !result.Events[1].Resumed {
		t.Fatalf("retry events do not reflect continuation: %#v", result.Events)
	}
	if diagnostic := initial.String(); diagnostic == "" || bytes.Contains([]byte(diagnostic), []byte("private-token")) {
		t.Fatalf("request diagnostic leaked a header value: %s", diagnostic)
	}
}

func TestLoginUsesOneForcedRefreshAttemptBeforeBuildWait(t *testing.T) {
	cfg := baseConfig(ModeBuild)
	cfg.Refreshable = true
	clock := cfg.Clock.(*testClock)
	calls := 0
	result, err := Run(context.Background(), Request{Provider: "chatgpt", Body: []byte("request")}, cfg, nil, func(_ context.Context, _ Request, mode AttemptMode) (Outcome, error) {
		calls++
		if calls == 1 && mode.ForceRefresh {
			t.Fatal("first dispatch cannot be a refresh replay")
		}
		if calls == 2 && !mode.ForceRefresh {
			t.Fatal("second dispatch must force one refresh")
		}
		if calls == 1 {
			return Outcome{}, &Failure{Class: ClassLogin}
		}
		return Outcome{Text: "ok"}, nil
	})
	if err != nil || calls != 2 || result.Attempts != 2 {
		t.Fatalf("login refresh retry calls=%d result=%#v err=%v", calls, result, err)
	}
	if len(clock.waits) != 0 {
		t.Fatalf("successful forced refresh should not pause: %v", clock.waits)
	}
}

func TestBuildUsageLimitWaitIsFiveMinutesAndOutsideWallBudget(t *testing.T) {
	clock := newTestClock()
	cfg := baseConfig(ModeBuild)
	cfg.Clock = clock
	cfg.Budget = NewWallBudget(clock, time.Minute)
	state := &State{}
	calls := 0
	result, err := Run(context.Background(), Request{Provider: "chatgpt"}, cfg, state, func(_ context.Context, _ Request, _ AttemptMode) (Outcome, error) {
		calls++
		clock.advance(10 * time.Second)
		return Outcome{}, &Failure{Class: ClassUsageLimit, RetryAfterMS: uint64Pointer(5_000)}
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Class != ClassUsageLimit {
		t.Fatalf("Build should return the stage restart cause after waiting: %v", err)
	}
	if !result.RestartStage || calls != 1 || state.WaitedMS != 300_000 {
		t.Fatalf("wait transition state = %#v, calls=%d", result, calls)
	}
	if !reflect.DeepEqual(clock.waits, []time.Duration{5 * time.Minute}) {
		t.Fatalf("Retry-After must not shorten Build pause: %v", clock.waits)
	}
	if remaining := cfg.Budget.Remaining(); remaining != 50*time.Second {
		t.Fatalf("provider wait consumed Build budget: %s remains", remaining)
	}
	if len(result.Events) != 1 || result.Events[0].Kind != "provider_wait" || !result.Events[0].BudgetPaused {
		t.Fatalf("missing budget-paused provider_wait event: %#v", result.Events)
	}
}

func TestShapeLoginStopsWithoutBuildWait(t *testing.T) {
	cfg := baseConfig(ModeShape)
	calls := 0
	result, err := Run(context.Background(), Request{Provider: "chatgpt"}, cfg, nil, func(context.Context, Request, AttemptMode) (Outcome, error) {
		calls++
		return Outcome{}, &Failure{Class: ClassLogin}
	})
	var failure *Failure
	if !errors.As(err, &failure) || calls != 1 || result.RestartStage || len(result.Events) != 0 {
		t.Fatalf("Shape login must exit without a ladder pause: calls=%d result=%#v err=%v", calls, result, err)
	}
}

func TestOverloadSwitchesAfterTwoConsecutiveErrorsAndDropsEncryptedPayload(t *testing.T) {
	cfg := baseConfig(ModeBuild)
	cfg.FallbackOn = true
	cfg.Hooks.Switch = func(_ context.Context, request Request, target contract.RoleSettings) (Request, error) {
		if len(request.History) != 1 || bytes.Contains(request.History[0], []byte("encrypted_content")) {
			t.Fatalf("encrypted reasoning was not removed on model change: %q", request.History)
		}
		request.Model = target.Model
		request.Effort = target.Effort
		request.Body = []byte("sol-request")
		return request, nil
	}
	initial := Request{
		Provider: "chatgpt",
		Model:    "gpt-6.1-luna",
		Effort:   "medium",
		Body:     []byte("luna-request"),
		History:  []json.RawMessage{json.RawMessage("{\"type\":\"reasoning\",\"encrypted_content\":\"private\",\"summary\":\"keep\"}")},
	}
	calls := 0
	result, err := Run(context.Background(), initial, cfg, nil, func(_ context.Context, request Request, _ AttemptMode) (Outcome, error) {
		calls++
		if calls <= 2 {
			return Outcome{}, &Failure{Class: ClassOverload}
		}
		if request.Model != "gpt-6.1-sol" || string(request.Body) != "sol-request" {
			t.Fatalf("fallback request tuple/body = %#v", request)
		}
		return Outcome{Text: "ok"}, nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("overload switch calls=%d result=%#v err=%v", calls, result, err)
	}
	if len(result.Events) != 2 || result.Events[1].Kind != "provider_switch" || result.Events[1].DelayMS != 0 || result.Events[1].FromModel != "gpt-6.1-luna/medium" || result.Events[1].ToModel != "gpt-6.1-sol/medium" {
		t.Fatalf("overload switch event = %#v", result.Events)
	}
}

func TestOverloadFallbackRoleAndProviderRules(t *testing.T) {
	current := contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-luna", Effort: "medium"}
	target, ok, err := ResolveOverloadFallback("builder", current, nil, true)
	if err != nil || !ok || target != (contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "medium"}) {
		t.Fatalf("default builder fallback = %#v, %t, %v", target, ok, err)
	}
	if _, ok, err := ResolveOverloadFallback("planner", current, nil, true); err != nil || ok {
		t.Fatalf("planner received a fallback: %t %v", ok, err)
	}
	current.Provider = "grok"
	if _, ok, err := ResolveOverloadFallback("builder", current, nil, true); err != nil || ok {
		t.Fatalf("Grok received a model fallback: %t %v", ok, err)
	}
	crossProvider := contract.RoleSettings{Provider: "grok", Model: "grok-4.6", Effort: "high"}
	if _, _, err := ResolveOverloadFallback("builder", contract.RoleSettings{Provider: "chatgpt", Model: "luna", Effort: "medium"}, &crossProvider, true); err == nil {
		t.Fatal("cross-provider model fallback was accepted")
	}
}

func TestShapeFallbackAliasesEffectiveShaperTuple(t *testing.T) {
	shaper := contract.RoleSettings{Provider: "grok", Model: "grok-4.6", Effort: "high"}
	manifest := contract.RoleManifest{Effective: map[contract.RoleName]contract.RoleSettings{"shaper": shaper}, FallbackShaper: shaper}
	got, err := ResolveShapeFallback(manifest)
	if err != nil || got != shaper {
		t.Fatalf("Grok Shape fallback should remain on Grok: %#v %v", got, err)
	}
	manifest.FallbackShaper = contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high"}
	if _, err := ResolveShapeFallback(manifest); err == nil {
		t.Fatal("a different-provider fallback_shaper override was accepted")
	}
}

func TestWaitCapDoesNotScheduleAnotherBuildPause(t *testing.T) {
	clock := newTestClock()
	cfg := baseConfig(ModeBuild)
	cfg.Clock = clock
	state := &State{WaitedMS: uint64(PauseLimit/time.Millisecond) - uint64(PauseDuration/time.Millisecond) + 1}
	result, err := Run(context.Background(), Request{Provider: "chatgpt"}, cfg, state, func(context.Context, Request, AttemptMode) (Outcome, error) {
		return Outcome{}, &Failure{Class: ClassLogin}
	})
	var failure *Failure
	if !errors.As(err, &failure) || result.RestartStage || len(clock.waits) != 0 {
		t.Fatalf("pause cap transition = %#v; sleeps=%v err=%v", result, clock.waits, err)
	}
}

func TestTestTimeScaleAppliesToRetryPausesAndBuildBudget(t *testing.T) {
	t.Setenv("KOGEN_TIME_SCALE", "0.02")
	if got := scaledDuration(5 * time.Minute); got != 6*time.Second {
		t.Fatalf("scaled retry wait = %s", got)
	}
	budget := NewWallBudget(nil, time.Minute)
	if budget.limit != 1200*time.Millisecond {
		t.Fatalf("scaled Build budget = %s", budget.limit)
	}
}

func uint64Pointer(value uint64) *uint64 { return &value }
