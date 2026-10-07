package retry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"kogen-go/internal/contract"
)

const (
	MaxAttempts   = 4
	PauseDuration = 5 * time.Minute
	PauseLimit    = 24 * time.Hour
)

const ContinuationInstruction = "The response stream was interrupted. Continue the same turn from the received progress above. Preserve its findings and constraints; do not restart the task or repeat completed work. Proposed tool calls above were not executed; reissue any still needed."

type Class string

const (
	ClassTimeout     Class = "timeout"
	ClassStall       Class = "stall"
	ClassTransport   Class = "transport"
	ClassOverload    Class = "overload"
	ClassMalformed   Class = "malformed"
	ClassUsageLimit  Class = "usage_limit"
	ClassLogin       Class = "login"
	ClassIncomplete  Class = "incomplete"
	ClassUnsupported Class = "unsupported"
	ClassCancelled   Class = "cancelled"
)

type Mode string

const (
	ModeBuild Mode = "build"
	ModeShape Mode = "shape"
)

// Failure is the common classified error passed from an adapter into this
// policy. Raw response progress remains in request history, not diagnostics.
type Failure struct {
	Class        Class
	Message      string
	Cause        error
	RetryAfterMS *uint64
	PartialItems []json.RawMessage
}

func (f *Failure) Error() string {
	if f == nil {
		return "<nil>"
	}
	if f.Message != "" {
		return f.Message
	}
	return string(f.Class)
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

func (f *Failure) RetryClass() string {
	if f == nil {
		return ""
	}
	return string(f.Class)
}

func (f *Failure) RetryAfterMillis() *uint64 {
	if f == nil || f.RetryAfterMS == nil {
		return nil
	}
	value := *f.RetryAfterMS
	return &value
}

func (f *Failure) RetryItems() []json.RawMessage {
	if f == nil {
		return nil
	}
	return cloneItems(f.PartialItems)
}

func (f *Failure) String() string {
	if f == nil {
		return "retry.Failure<nil>"
	}
	return fmt.Sprintf("retry.Failure{class=%q partial_items=%d}", f.Class, len(f.PartialItems))
}

func (f *Failure) GoString() string { return f.String() }

type Request struct {
	Provider string
	Model    string
	Effort   string
	Body     []byte
	Headers  http.Header
	History  []json.RawMessage
	Metadata any
}

func (r Request) String() string {
	return fmt.Sprintf("retry.Request{provider=%q model=%q effort=%q body_bytes=%d history_items=%d header_names=%q}", r.Provider, r.Model, r.Effort, len(r.Body), len(r.History), headerNames(r.Headers))
}

func (r Request) GoString() string { return r.String() }

type Outcome struct {
	Value    any
	RawItems []json.RawMessage
	Text     string
}

type AttemptMode struct {
	Number       int
	ForceRefresh bool
}

type AttemptFunc func(context.Context, Request, AttemptMode) (Outcome, error)

type Hooks struct {
	// Continue appends the received raw items and ContinuationInstruction to
	// the previous full input. It returns the canonically serialized request.
	Continue func(context.Context, Request, []json.RawMessage, string) (Request, error)
	// Switch rebuilds the request for target. History has prior-model encrypted
	// payloads removed, while summaries and other items remain.
	Switch func(context.Context, Request, contract.RoleSettings) (Request, error)
}

type Config struct {
	Mode        Mode
	Role        contract.RoleName
	Current     contract.RoleSettings
	Fallback    *contract.RoleSettings
	FallbackOn  bool
	Refreshable bool
	Budget      *WallBudget
	Clock       contract.Clock
	Jitter      contract.JitterSource
	Hooks       Hooks
}

// State is retained by a Build across login/usage-limit pauses. Other retry
// counters are scoped to one logical request and reset when Run is called.
type State struct {
	WaitedMS uint64
}

type Event struct {
	Kind         string
	Reason       string
	DelayMS      uint64
	PausedMS     uint64
	Resumed      bool
	BudgetPaused bool
	FromModel    string
	ToModel      string
}

type Result struct {
	Outcome      Outcome
	Attempts     int
	Events       []Event
	RawItems     []json.RawMessage
	TextPrefix   string
	Resumed      bool
	RestartStage bool
	WaitedMS     uint64
}

func (r Result) String() string {
	return fmt.Sprintf("retry.Result{attempts=%d events=%d raw_items=%d resumed=%t restart_stage=%t waited_ms=%d}", r.Attempts, len(r.Events), len(r.RawItems), r.Resumed, r.RestartStage, r.WaitedMS)
}

func (r Result) GoString() string { return r.String() }

// WallBudget measures active Build time. Provider request time consumes it;
// provider backoff and login/usage-limit waits use Pause and do not.
type WallBudget struct {
	mu           sync.Mutex
	clock        contract.Clock
	limit        time.Duration
	started      time.Time
	paused       time.Duration
	pauseDepth   int
	pauseStarted time.Time
}

func NewWallBudget(clock contract.Clock, limit time.Duration) *WallBudget {
	if clock == nil {
		clock = realClock{}
		limit = scaledDuration(limit)
	}
	now := clock.Now()
	return &WallBudget{clock: clock, limit: limit, started: now}
}

func (b *WallBudget) Remaining() time.Duration {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()
	elapsed := now.Sub(b.started) - b.paused
	if b.pauseDepth > 0 {
		elapsed -= now.Sub(b.pauseStarted)
	}
	if elapsed < 0 {
		elapsed = 0
	}
	remaining := b.limit - elapsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (b *WallBudget) pause(ctx context.Context, delay time.Duration) (time.Duration, error) {
	if b == nil {
		return 0, errors.New("cannot pause a missing Build budget")
	}
	b.mu.Lock()
	if b.pauseDepth == 0 {
		b.pauseStarted = b.clock.Now()
	}
	b.pauseDepth++
	b.mu.Unlock()

	started := b.clock.Now()
	err := b.clock.Sleep(ctx, delay)
	elapsed := nonnegative(b.clock.Now().Sub(started))

	b.mu.Lock()
	b.pauseDepth--
	if b.pauseDepth == 0 {
		b.paused += nonnegative(b.clock.Now().Sub(b.pauseStarted))
		b.pauseStarted = time.Time{}
	}
	b.mu.Unlock()
	return elapsed, err
}

func (b *WallBudget) attemptContext(ctx context.Context) (context.Context, context.CancelFunc, bool) {
	remaining := b.Remaining()
	if remaining <= 0 {
		return ctx, func() {}, false
	}
	attemptCtx, cancel := context.WithTimeout(ctx, remaining)
	return attemptCtx, cancel, true
}

// Run executes a logical provider request and owns every transient retry.
// Callers must stop on a returned transient Failure; retrying it elsewhere
// would create a forbidden second retry layer. A Build login/usage wait returns
// RestartStage after pausing so the Build controller can restart that stage.
func Run(ctx context.Context, initial Request, cfg Config, state *State, attempt AttemptFunc) (result Result, retErr error) {
	result = Result{}
	if attempt == nil {
		return result, errors.New("provider retry requires an attempt function")
	}
	if cfg.Mode != ModeBuild && cfg.Mode != ModeShape {
		return result, errors.New("provider retry mode must be build or shape")
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.Jitter == nil {
		cfg.Jitter = secureJitter{}
	}
	if state == nil {
		state = &State{}
	}

	current := cloneRequest(initial)
	retryAttempt := 1
	overloadStreak := 0
	refreshed := false
	forceRefresh := false
	var continuedItems []json.RawMessage
	var pendingItems []json.RawMessage
	var continuedText strings.Builder
	resumed := false
	defer func() {
		if retErr != nil || result.RawItems == nil {
			result.RawItems = append(cloneItems(continuedItems), cloneItems(pendingItems)...)
		}
		result.TextPrefix = continuedText.String()
		if len(pendingItems) > 0 {
			result.TextPrefix += textFromItems(pendingItems)
		}
		result.Resumed = resumed
		if state != nil {
			result.WaitedMS = state.WaitedMS
		}
	}()

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		attemptCtx := ctx
		cancelAttempt := func() {}
		if cfg.Budget != nil {
			var ok bool
			attemptCtx, cancelAttempt, ok = cfg.Budget.attemptContext(ctx)
			if !ok {
				return result, &Failure{Class: ClassTimeout, Message: "provider/build budget exhausted"}
			}
		}
		result.Attempts++
		outcome, err := attempt(attemptCtx, cloneRequest(current), AttemptMode{Number: result.Attempts, ForceRefresh: forceRefresh})
		cancelAttempt()
		if err == nil {
			outcome.RawItems = append(cloneItems(continuedItems), cloneItems(outcome.RawItems)...)
			outcome.Text = continuedText.String() + outcome.Text
			result.Outcome = outcome
			result.RawItems = cloneItems(outcome.RawItems)
			pendingItems = nil
			return result, nil
		}
		failure := classifyFailure(err)
		if failure == nil {
			return result, err
		}
		pendingItems = cloneItems(failure.PartialItems)
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if failure.Class == ClassCancelled {
			return result, err
		}

		if failure.Class == ClassLogin && cfg.Refreshable && !refreshed {
			refreshed = true
			forceRefresh = true
			continue
		}
		forceRefresh = false

		if failure.Class == ClassLogin || failure.Class == ClassUsageLimit {
			if cfg.Mode == ModeBuild {
				return pauseForStage(ctx, result, cfg, state, failure)
			}
			return result, err
		}
		if !isTransient(failure.Class) {
			return result, err
		}

		if failure.Class == ClassOverload {
			overloadStreak++
		} else {
			overloadStreak = 0
		}
		target, canSwitch, fallbackErr := ResolveOverloadFallback(cfg.Role, cfg.Current, cfg.Fallback, cfg.FallbackOn)
		if fallbackErr != nil {
			return result, fallbackErr
		}
		modelSwitchAvailable := canSwitch && cfg.Hooks.Switch != nil
		switching := failure.Class == ClassOverload && overloadStreak >= 2 && modelSwitchAvailable
		bounded := cfg.Budget != nil
		unlimitedClass := failure.Class == ClassTimeout || failure.Class == ClassStall || failure.Class == ClassTransport || (failure.Class == ClassOverload && !modelSwitchAvailable)
		attemptsLeft := (bounded && unlimitedClass) || retryAttempt < MaxAttempts

		if switching {
			if !attemptsLeft || (bounded && cfg.Budget.Remaining() <= 0) {
				return result, err
			}
			before := modelIdentity(current.Model, current.Effort)
			clean := cloneRequest(current)
			clean.History = DropEncryptedReasoning(clean.History)
			next, switchErr := cfg.Hooks.Switch(ctx, clean, target)
			if switchErr != nil {
				return result, switchErr
			}
			if next.Provider != current.Provider || next.Model != target.Model || next.Effort != target.Effort {
				return result, errors.New("provider model-switch hook returned an incompatible role")
			}
			current = cloneRequest(next)
			cfg.Current = target
			overloadStreak = 0
			retryAttempt++
			result.Events = append(result.Events, Event{
				Kind:      "provider_switch",
				Reason:    "provider/overload",
				FromModel: before,
				ToModel:   modelIdentity(target.Model, target.Effort),
			})
			continue
		}

		if !attemptsLeft {
			return result, err
		}
		ceiling := BackoffCeiling(retryAttempt)
		delay, jitterErr := jitterDelay(cfg.Jitter, ceiling)
		if jitterErr != nil {
			return result, jitterErr
		}
		delayDuration := time.Duration(delay) * time.Millisecond
		if bounded && cfg.Budget.Remaining() <= clockDuration(cfg.Clock, delayDuration) {
			return result, err
		}

		continuable := hasPartialProgress(failure) && canContinue(failure.Class)
		if continuable {
			if cfg.Hooks.Continue == nil {
				return result, errors.New("provider response has partial items but continuation is unavailable")
			}
			partial := cloneItems(failure.PartialItems)
			next, continueErr := cfg.Hooks.Continue(ctx, cloneRequest(current), partial, ContinuationInstruction)
			if continueErr != nil {
				return result, continueErr
			}
			if next.Provider != current.Provider {
				return result, errors.New("provider continuation changed provider")
			}
			current = cloneRequest(next)
			continuedItems = append(continuedItems, partial...)
			pendingItems = nil
			continuedText.WriteString(textFromItems(partial))
			resumed = true
		}
		paused, sleepErr := pause(ctx, cfg.Clock, cfg.Budget, delayDuration)
		if sleepErr != nil {
			result.Events = append(result.Events, Event{
				Kind: "provider_retry", Reason: "provider/" + string(failure.Class),
				DelayMS: delay, PausedMS: durationMillis(paused), Resumed: resumed,
				BudgetPaused: cfg.Budget != nil,
			})
			return result, sleepErr
		}
		result.Events = append(result.Events, Event{
			Kind: "provider_retry", Reason: "provider/" + string(failure.Class),
			DelayMS: delay, PausedMS: durationMillis(paused), Resumed: resumed,
			BudgetPaused: cfg.Budget != nil,
		})
		retryAttempt++
	}
}

func pauseForStage(ctx context.Context, result Result, cfg Config, state *State, failure *Failure) (Result, error) {
	pauseMS := uint64(PauseDuration / time.Millisecond)
	pauseLimitMS := uint64(PauseLimit / time.Millisecond)
	if state.WaitedMS > pauseLimitMS || pauseMS > pauseLimitMS-state.WaitedMS {
		return result, failure
	}
	paused, err := pause(ctx, cfg.Clock, cfg.Budget, PauseDuration)
	if err != nil {
		state.WaitedMS += durationMillis(paused)
		result.Events = append(result.Events, Event{
			Kind: "provider_wait", Reason: "provider/" + string(failure.Class),
			DelayMS: pauseMS, PausedMS: durationMillis(paused), BudgetPaused: true,
		})
		return result, err
	}
	state.WaitedMS += pauseMS
	result.Events = append(result.Events, Event{
		Kind: "provider_wait", Reason: "provider/" + string(failure.Class),
		DelayMS: pauseMS, PausedMS: durationMillis(paused), BudgetPaused: true,
	})
	result.RestartStage = true
	return result, failure
}

func pause(ctx context.Context, clock contract.Clock, budget *WallBudget, delay time.Duration) (time.Duration, error) {
	if budget != nil {
		return budget.pause(ctx, delay)
	}
	started := clock.Now()
	err := clock.Sleep(ctx, delay)
	return nonnegative(clock.Now().Sub(started)), err
}

func isTransient(class Class) bool {
	switch class {
	case ClassTimeout, ClassStall, ClassTransport, ClassOverload, ClassMalformed:
		return true
	default:
		return false
	}
}

func canContinue(class Class) bool {
	switch class {
	case ClassTimeout, ClassStall, ClassTransport, ClassMalformed:
		return true
	default:
		return false
	}
}

func hasPartialProgress(failure *Failure) bool {
	if failure == nil {
		return false
	}
	for _, item := range failure.PartialItems {
		if json.Valid(item) {
			return true
		}
	}
	return false
}

func classifyFailure(err error) *Failure {
	var failure *Failure
	if errors.As(err, &failure) && failure != nil {
		return cloneFailure(failure)
	}
	var classified interface{ RetryClass() string }
	if !errors.As(err, &classified) {
		return nil
	}
	class := Class(classified.RetryClass())
	if class == "" {
		return nil
	}
	failure = &Failure{Class: class, Cause: err, Message: err.Error()}
	var retryAfter interface{ RetryAfterMillis() *uint64 }
	if errors.As(err, &retryAfter) {
		failure.RetryAfterMS = retryAfter.RetryAfterMillis()
	}
	var items interface{ RetryItems() []json.RawMessage }
	if errors.As(err, &items) {
		failure.PartialItems = items.RetryItems()
	}
	return failure
}

func cloneFailure(failure *Failure) *Failure {
	copy := *failure
	copy.PartialItems = cloneItems(failure.PartialItems)
	if failure.RetryAfterMS != nil {
		value := *failure.RetryAfterMS
		copy.RetryAfterMS = &value
	}
	return &copy
}

func cloneRequest(request Request) Request {
	request.Body = bytes.Clone(request.Body)
	request.Headers = request.Headers.Clone()
	request.History = cloneItems(request.History)
	return request
}

func cloneItems(items []json.RawMessage) []json.RawMessage {
	cloned := make([]json.RawMessage, len(items))
	for i := range items {
		cloned[i] = bytes.Clone(items[i])
	}
	return cloned
}

func BackoffCeiling(failedAttempt int) uint64 {
	switch failedAttempt {
	case 0, 1:
		return 2_000
	case 2:
		return 4_000
	case 3:
		return 8_000
	case 4:
		return 16_000
	case 5:
		return 32_000
	default:
		return 60_000
	}
}

func jitterDelay(jitter contract.JitterSource, ceiling uint64) (uint64, error) {
	floor := ceiling / 2
	draw, err := jitter.Uint64n(ceiling - floor + 1)
	if err != nil {
		return 0, err
	}
	return floor + draw, nil
}

// ResolveOverloadFallback applies the provider and role exclusions for an
// overload-driven model switch. Shape's fresh fallback conversation is
// resolved separately through the role manifest.
func ResolveOverloadFallback(role contract.RoleName, current contract.RoleSettings, configured *contract.RoleSettings, enabled bool) (contract.RoleSettings, bool, error) {
	if configured != nil && configured.Provider != "" && current.Provider != "" && configured.Provider != current.Provider {
		return contract.RoleSettings{}, false, errors.New("provider model fallback cannot cross providers")
	}
	switch role {
	case "builder", "context", "reviewer":
	default:
		return contract.RoleSettings{}, false, nil
	}
	if !enabled || current.Provider != "chatgpt" {
		return contract.RoleSettings{}, false, nil
	}
	target := contract.RoleSettings{Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "medium"}
	if configured != nil {
		if configured.Provider != current.Provider || configured.Model == "" || configured.Effort == "" {
			return contract.RoleSettings{}, false, errors.New("provider model fallback role is incomplete or cross-provider")
		}
		target = *configured
	}
	if current.Model == target.Model && current.Effort == target.Effort {
		return target, false, nil
	}
	return target, true, nil
}

// ResolveShapeFallback verifies the draft alias rule at the role-manifest
// boundary: the fresh Shape conversation inherits the effective shaper tuple.
func ResolveShapeFallback(manifest contract.RoleManifest) (contract.RoleSettings, error) {
	shaper, ok := manifest.Effective[contract.RoleName("shaper")]
	if !ok || shaper.Provider == "" || shaper.Model == "" || shaper.Effort == "" {
		return contract.RoleSettings{}, errors.New("effective shaper role is unavailable")
	}
	fallback := manifest.FallbackShaper
	if fallback == (contract.RoleSettings{}) {
		return shaper, nil
	}
	if fallback != shaper {
		return contract.RoleSettings{}, errors.New("fallback_shaper must alias the effective shaper role")
	}
	return shaper, nil
}

// DropEncryptedReasoning removes only encrypted payload fields. Summaries and
// all other history items remain available after a model switch.
func DropEncryptedReasoning(items []json.RawMessage) []json.RawMessage {
	out := cloneItems(items)
	for i, raw := range out {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			continue
		}
		var kind string
		if json.Unmarshal(item["type"], &kind) != nil || kind != "reasoning" {
			continue
		}
		if _, ok := item["encrypted_content"]; !ok {
			continue
		}
		delete(item, "encrypted_content")
		clean, err := json.Marshal(item)
		if err == nil {
			out[i] = clean
		}
	}
	return out
}

func textFromItems(items []json.RawMessage) string {
	var text strings.Builder
	for _, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			continue
		}
		var kind string
		if json.Unmarshal(item["type"], &kind) != nil || kind != "message" {
			continue
		}
		var content []json.RawMessage
		if json.Unmarshal(item["content"], &content) != nil {
			continue
		}
		for _, rawPart := range content {
			var part map[string]json.RawMessage
			if json.Unmarshal(rawPart, &part) != nil {
				continue
			}
			var partKind, value string
			if json.Unmarshal(part["type"], &partKind) == nil && partKind == "output_text" && json.Unmarshal(part["text"], &value) == nil {
				text.WriteString(value)
			}
		}
	}
	return text.String()
}

func modelIdentity(model, effort string) string { return model + "/" + effort }

func durationMillis(duration time.Duration) uint64 {
	if duration <= 0 {
		return 0
	}
	return uint64(duration / time.Millisecond)
}

func nonnegative(duration time.Duration) time.Duration {
	if duration < 0 {
		return 0
	}
	return duration
}

func headerNames(headers http.Header) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, http.CanonicalHeaderKey(name))
	}
	slices.Sort(names)
	return names
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(scaledDuration(duration))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func clockDuration(clock contract.Clock, duration time.Duration) time.Duration {
	if _, ok := clock.(realClock); ok {
		return scaledDuration(duration)
	}
	return duration
}

func scaledDuration(duration time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv("KOGEN_TIME_SCALE"))
	if raw == "" {
		return duration
	}
	scale, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(scale) || math.IsInf(scale, 0) || scale <= 0 {
		return duration
	}
	scaled := float64(duration) * scale
	if scaled < 1 {
		return time.Nanosecond
	}
	if scaled > float64(int64(^uint64(0)>>1)) {
		return time.Duration(int64(^uint64(0) >> 1))
	}
	return time.Duration(scaled)
}

type secureJitter struct{}

func (secureJitter) Uint64n(upperExclusive uint64) (uint64, error) {
	if upperExclusive == 0 {
		return 0, errors.New("jitter range must be positive")
	}
	value, err := rand.Int(rand.Reader, new(big.Int).SetUint64(upperExclusive))
	if err != nil {
		return 0, err
	}
	return value.Uint64(), nil
}
