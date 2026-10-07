package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/intent"
	providersession "kogen-go/internal/provider/session"
	"kogen-go/internal/provider/sse"
	"kogen-go/internal/safefs"
	"kogen-go/internal/shape/audit"
	"kogen-go/internal/shape/prompts"
	shapesession "kogen-go/internal/shape/session"
	"kogen-go/internal/shape/validate"
)

const (
	TranscriptFileName = "transcript.jsonl"
	intentDirectory    = ".kogen/intents"
)

var (
	ErrInvalidOptions        = errors.New("shape run: invalid options")
	ErrMissingCallTelemetry  = errors.New("shape run: successful turn has no model-call telemetry")
	ErrNoRequestDispatched   = errors.New("shape run: provider turn returned without an HTTP request")
	ErrInvalidTurnResponse   = errors.New("shape run: provider turn returned malformed response data")
	ErrInvalidToolResult     = errors.New("shape run: tool executor returned mismatched outputs")
	ErrRoleManifestMismatch  = errors.New("shape run: request does not match resolved role manifest")
	ErrValidationNotComplete = errors.New("shape run: validation returned neither success nor a candidate failure")
)

var runSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// AttemptAccounting is called at each physical HTTP boundary. Implementations
// must call Complete for every successful or failed dispatch. The runner closes
// a forgotten in-flight attempt as unknown usage before the turn ends.
type AttemptAccounting interface {
	Dispatch(shapesession.AttemptKind) error
	RecordRequest(contract.RequestEvidence) error
	Complete(*contract.TokenUsage) error
}

// Turner implements provider-specific request encoding, retries, streaming,
// and response decoding for one shaper logical turn. It uses the same
// Conversation pointer for all attempts and raw response history. Partial
// response items from interrupted attempts must be appended to that pointer
// before requesting a continuation. The final complete response items are
// returned in TurnResponse and appended by this package. RecordRequest must be
// called for each physical HTTP request, including requests that fail after
// dispatch; calls are also returned when the turn ends with an error.
type Turner interface {
	Turn(context.Context, TurnRequest, AttemptAccounting) (TurnResponse, error)
}

// TurnRequest carries the resolved shaper identity, immutable role prompt, and
// per-turn tool allowlist. The generic instruction/schema prefix is owned by
// the provider adapter; role instructions and turn history stay after it.
type TurnRequest struct {
	Conversation *providersession.Conversation
	Role         contract.RoleName
	Settings     contract.RoleSettings
	Instructions string
	AllowedTools []string
	ToolChoice   string
}

// ToolExecutor runs all calls from one response in order. Expected tool errors
// are returned as ordinary output strings. A Go error means controller or
// environment failure and stops Shape.
type ToolExecutor interface {
	Execute(context.Context, []sse.ToolCall) ([]ToolOutput, error)
}

// ToolOutput is one tool result matched to its provider call id.
type ToolOutput struct {
	CallID string
	Output string
}

// TurnResponse is one completed provider logical turn. RawItems retain the
// provider's original response objects; they are never re-created from Text or
// ToolCalls. Calls contains the safe aggregate/transport observations for that
// turn, including one record per model call when the provider switches models.
type TurnResponse struct {
	Text      string
	ToolCalls []sse.ToolCall
	RawItems  []json.RawMessage
	Calls     []ModelCall
}

// ModelCall describes one logical model call for the stable text output and
// records every physical request attempt in the private transcript.
type ModelCall struct {
	Role     contract.RoleName
	Settings contract.RoleSettings
	Usage    *contract.TokenUsage
	WallMS   uint64
	Requests []contract.RequestEvidence
}

// Options contains the already-resolved public Shape inputs and injected
// effects. StartedAt should be captured at command entry, before project setup
// or validation. ScratchRoot is the descriptor-rooted private Shape scratch
// directory used for the transcript and accounting receipt.
type Options struct {
	Checkout      string
	Slug          string
	Request       []byte
	RequestSource string
	RequestIssue  string
	Domains       []string
	GatePaths     []string

	Manifest contract.RoleManifest
	Factory  shapesession.ConversationFactory
	Turner   Turner
	Tools    ToolExecutor

	Validation     validate.Options
	AuditTransport audit.Transport

	ScratchDir  string
	ScratchRoot *safefs.Root
	Roots       contract.RootOpener

	StartedAt time.Time
	Now       func() time.Time
	Progress  func(string)
}

// Runner owns one Shape operation. Instances are sequential and must not be
// reused for another command.
type Runner struct {
	options              Options
	session              *shapesession.Run
	calls                []ModelCall
	fallbackEventWritten bool
}

// New validates provider-session accounting and captures command-entry time.
// Full project, adapter, and request validation occurs in Execute so those
// failures can still publish shape-accounting.json.
func New(options Options) (*Runner, error) {
	if options.ScratchRoot == nil || options.ScratchDir == "" || !cleanAbsolute(options.ScratchDir) || options.Checkout == "" || !cleanAbsolute(options.Checkout) {
		return nil, ErrInvalidOptions
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	started := options.StartedAt
	if started.IsZero() {
		started = now()
	}
	options.Request = append([]byte(nil), options.Request...)
	options.Domains = append([]string(nil), options.Domains...)
	options.GatePaths = append([]string(nil), options.GatePaths...)
	sort.Strings(options.Domains)
	sort.Strings(options.GatePaths)
	sessionOptions := shapesession.Options{
		Manifest: options.Manifest, Factory: options.Factory,
		StartedAt: started, Now: now,
	}
	state, err := shapesession.New(sessionOptions)
	if err != nil {
		return nil, fmt.Errorf("shape run: initialize accounting: %w", err)
	}
	return &Runner{options: options, session: state}, nil
}

// Execute performs Shape setup, provider turns, validation and both eligible
// audits. It always attempts to publish accounting after Runner construction.
func (r *Runner) Execute(ctx context.Context) (result Result, returnedErr error) {
	if r == nil || r.session == nil {
		return Result{}, ErrInvalidOptions
	}
	if ctx == nil {
		ctx = context.Background()
	}
	outcome := shapesession.OutcomeFailure
	defer func() {
		if publishErr := r.session.PublishAccounting(r.options.ScratchRoot, outcome); publishErr != nil {
			publishFailure := failure("environment", "shape_scratch_unavailable", "cannot publish shape accounting", 3, publishErr)
			returnedErr = errors.Join(returnedErr, publishFailure)
		}
	}()

	if !runSlugPattern.MatchString(r.options.Slug) || len(r.options.Slug) < 3 || len(r.options.Slug) > 48 {
		return Result{}, failure("intent", "invalid_slug", "Slug must use lowercase letters, digits, and dashes.", 2, nil)
	}
	if err := r.validateRequest(); err != nil {
		return Result{}, err
	}
	validator, capture, err := r.newValidator()
	if err != nil {
		return Result{}, r.validationConstructionFailure(err)
	}
	if r.options.Turner == nil || r.options.Tools == nil || r.options.AuditTransport == nil {
		return Result{}, failure("controller", "shape_run_unavailable", "Shape provider, tool, and audit ports are required", 70, ErrInvalidOptions)
	}
	paths := capture.PathsSnapshot()
	if paths.Source == "" || paths.Candidate == "" {
		return Result{}, failure("environment", "shape_output_unavailable", "acceptance adapter returned no paths", 3, ErrInvalidOptions)
	}
	intentPath := path.Join(intentDirectory, r.options.Slug, "intent.md")
	checkoutRoot, err := r.openRoot(r.options.Checkout)
	if err != nil {
		return Result{}, failure("environment", "shape_output_unavailable", "cannot open project checkout", 3, err)
	}
	defer closeRoot(checkoutRoot)
	for _, directory := range []string{path.Dir(intentPath), path.Dir(paths.Source)} {
		if err := checkoutRoot.MkdirAll(directory, 0o755); err != nil {
			return Result{}, failure("environment", "shape_output_unavailable", "cannot prepare Shape output directory", 3, err)
		}
	}
	if err := validator.Prepare(ctx); err != nil {
		return Result{}, classifyEffectError(err, "setup_failed", 3)
	}
	firstMessage := prompts.InitialMessage(r.options.Slug, r.options.Domains, r.options.GatePaths,
		r.options.Request, intentPath, paths.Source)
	if err := r.session.StartPrimary(firstMessage); err != nil {
		return Result{}, r.classifyProviderOrController(err)
	}
	evaluator, err := audit.New(r.options.Manifest, r.options.AuditTransport, r.session)
	if err != nil {
		return Result{}, failure("controller", "shape_run_unavailable", "cannot initialize Shape audits", 70, err)
	}
	validationWarnings := make([]validate.Warning, 0)
	styleRepairs := [2]int{}
	var seenConcerns = make(map[string]struct{})
	for {
		pass, beginErr := r.session.BeginPass()
		if beginErr != nil {
			if shapesession.IsBudgetExhausted(beginErr) {
				var exhausted *shapesession.BudgetExhaustedError
				if r.session.FallbackStarted() && errors.As(beginErr, &exhausted) && exhausted.Conversation == 1 {
					r.emitFallbackStarted(passForFallback())
					continue
				}
				return Result{}, r.exhaustionFailure(beginErr)
			}
			return Result{}, r.classifyProviderOrController(beginErr)
		}
		identity := r.session.Session().Identity()
		role := identity.Role
		conversationIndex := 1
		if role == "fallback_shaper" {
			conversationIndex = 2
			r.emitFallbackStarted(pass)
		}
		r.emitProgress(fmt.Sprintf("shaper pass=%d role=%s started", pass, role))

		for {
			finalText, driveErr := r.drivePass(ctx, checkoutRoot, intentPath, paths.Source)
			if driveErr != nil {
				if shapesession.IsBudgetExhausted(driveErr) {
					if r.session.FallbackStarted() && role == "shaper" {
						r.emitFallbackStarted(pass)
						break
					}
					return Result{}, r.exhaustionFailure(driveErr)
				}
				return Result{}, r.classifyProviderOrController(driveErr)
			}
			r.recordConcerns(finalText, seenConcerns, &validationWarnings)
			r.emitProgress(fmt.Sprintf("shaper pass=%d role=%s complete turns=%d", pass, role, r.conversationTurns(conversationIndex)))
			if err := r.session.BeginValidationTraversal(); err != nil {
				return Result{}, r.classifyProviderOrController(err)
			}
			validated, validateErr := validator.Validate(ctx, pass, string(role), styleRepairs[conversationIndex-1])
			if validateErr != nil {
				return Result{}, classifyEffectError(validateErr, "shape_validation_failed", 3)
			}
			validationWarnings = appendWarnings(validationWarnings, validated.Warnings...)
			for _, progress := range validated.Progress {
				r.emitProgress(progress)
			}
			if len(validated.StyleRepair) != 0 {
				allowed, styleErr := r.session.RecordStyleTraversal()
				if styleErr != nil {
					return Result{}, r.classifyProviderOrController(styleErr)
				}
				if allowed {
					styleRepairs[conversationIndex-1]++
					r.emitProgress(fmt.Sprintf("shaper pass=%d role=%s style_repair", pass, role))
					if err := r.session.Session().AppendControllerMessage(styleRepairMessage(validated.StyleRepair)); err != nil {
						return Result{}, failure("environment", "shape_output_unavailable", "cannot append style repair feedback", 3, err)
					}
					continue
				}
			}
			if len(validated.StyleRepair) == 0 && styleRepairs[conversationIndex-1] >= shapesession.MaxStyleRepairsPerConversation && hasLintWarning(validated.Warnings) {
				if _, styleErr := r.session.RecordStyleTraversal(); styleErr != nil {
					return Result{}, r.classifyProviderOrController(styleErr)
				}
			}
			if validated.Failure != nil {
				if validated.Failure.Reason == "acceptance_check_unavailable" || validated.Failure.Reason == "tool_missing" {
					return Result{}, failure("environment", validated.Failure.Reason, validated.Failure.Detail, 3, nil)
				}
				if err := r.finishCandidateFailure(pass, role, intentPath, paths.Source, validated.Failure, shapesession.RepairCandidate); err != nil {
					return Result{}, err
				}
				break
			}
			if !validated.Validated || validated.ParsedIntent == nil {
				return Result{}, failure("controller", "shape_validation_failed", "validation did not complete", 70, ErrValidationNotComplete)
			}

			auditResult, auditErr := evaluator.RunPass(ctx, audit.PassInput{
				Request: r.options.Request, Intent: validated.ParsedIntent,
				IntentBytes: validated.IntentBytes, TestBytes: validated.TestBytes,
				BaseResults: capture.Results(),
			})
			if auditErr != nil {
				if candidate := auditResponseFailure(auditErr); candidate != nil {
					if err := r.finishCandidateFailure(pass, role, intentPath, paths.Source, candidate, shapesession.RepairCandidate); err != nil {
						return Result{}, err
					}
					break
				}
				return Result{}, r.classifyProviderOrController(auditErr)
			}
			if err := r.publishAuditArtifacts(checkoutRoot, intentPath, r.auditWarnings(validationWarnings, auditResult.Warnings), auditResult); err != nil {
				return Result{}, failure("environment", "shape_output_unavailable", "cannot publish Shape audit artifacts", 3, err)
			}
			validationWarnings = mergeWarnings(validationWarnings, convertAuditWarnings(auditResult.Warnings)...)
			if auditResult.Repair != nil {
				if err := r.finishCandidateFailure(pass, role, intentPath, paths.Source,
					&validate.Failure{Reason: auditResult.Repair.Reason, Detail: auditResult.Repair.Detail}, auditResult.Repair.Kind); err != nil {
					return Result{}, err
				}
				break
			}

			if err := r.session.CountValidationPass(); err != nil {
				return Result{}, r.classifyProviderOrController(err)
			}
			if err := r.session.CompletePass(); err != nil {
				return Result{}, r.classifyProviderOrController(err)
			}
			if r.session.FallbackStarted() {
				r.emitFallbackStarted(pass)
			}
			r.emitProgress(fmt.Sprintf("shaper pass=%d role=%s validation_passed", pass, role))
			result, resultErr := r.successResult(intentPath, paths.Source, validationWarnings)
			if resultErr != nil {
				return Result{}, r.classifyProviderOrController(resultErr)
			}
			outcome = shapesession.OutcomeSuccess
			return result, nil
		}
		if r.session.FallbackStarted() && r.session.Session() != nil && r.session.Session().Identity().Role == "fallback_shaper" {
			continue
		}
	}
}

func (r *Runner) newValidator() (*validate.Validator, *captureAdapter, error) {
	if r.options.Validation.Adapter == nil {
		return nil, nil, fmt.Errorf("validation adapter is required: %w", ErrInvalidOptions)
	}
	capture := &captureAdapter{delegate: r.options.Validation.Adapter}
	options := r.options.Validation
	options.Checkout = r.options.Checkout
	options.Slug = r.options.Slug
	options.Request = append([]byte(nil), r.options.Request...)
	options.GatePaths = append([]string(nil), r.options.GatePaths...)
	options.Adapter = capture
	if options.Roots == nil {
		options.Roots = r.options.Roots
	}
	validator, err := validate.New(options)
	if err != nil {
		return nil, nil, err
	}
	return validator, capture, nil
}

func (r *Runner) validateRequest() error {
	source := r.options.RequestSource
	if source == "" || source == "-" {
		source = "stdin"
	}
	switch r.options.RequestIssue {
	case "":
	case "empty", "not found", "unreadable":
		return failure("intent", "request_unavailable", source+": "+r.options.RequestIssue, 2, nil)
	default:
		return failure("controller", "internal_error", "invalid request error state", 70, ErrInvalidOptions)
	}
	if len(r.options.Request) == 0 || len(strings.TrimSpace(string(r.options.Request))) == 0 {
		return failure("intent", "request_unavailable", source+": empty", 2, nil)
	}
	if len(r.options.Domains) == 0 {
		return failure("environment", "project_config_invalid", "configured project domains are unavailable", 3, nil)
	}
	return nil
}

func (r *Runner) drivePass(ctx context.Context, checkoutRoot contract.RootedFS, intentPath, acceptancePath string) (string, error) {
	for {
		conversation := r.session.Session()
		if conversation == nil {
			return "", shapesession.ErrNoConversation
		}
		identity := conversation.Identity()
		meter := &attemptMeter{session: r.session, identity: identity}
		response, turnErr := r.options.Turner.Turn(ctx, TurnRequest{
			Conversation: conversation, Role: identity.Role,
			Settings:     contract.RoleSettings{Provider: identity.Provider, Model: identity.Model, Effort: identity.Effort},
			Instructions: prompts.SystemPrompt(), AllowedTools: []string{"read", "search", "write"}, ToolChoice: "auto",
		}, meter)
		turnErr = errors.Join(turnErr, meter.finish())
		calls, telemetryErr := meter.normalizeCalls(response.Calls)
		if telemetryErr != nil {
			return "", telemetryErr
		}
		if meter.attempts != 0 && len(calls) == 0 {
			telemetryErr = ErrMissingCallTelemetry
		}
		if telemetryErr != nil && turnErr != nil {
			return "", errors.Join(turnErr, telemetryErr)
		}
		if telemetryErr != nil {
			return "", telemetryErr
		}
		if err := r.recordCalls(identity, calls); err != nil {
			return "", err
		}
		if turnErr != nil {
			return "", turnErr
		}
		if !meter.started {
			return "", ErrNoRequestDispatched
		}
		if len(calls) == 0 {
			return "", ErrMissingCallTelemetry
		}
		if len(response.RawItems) == 0 {
			return "", ErrInvalidTurnResponse
		}
		for _, raw := range response.RawItems {
			if !json.Valid(raw) {
				return "", ErrInvalidTurnResponse
			}
			if err := conversation.Append(contract.SessionResponseItem, raw); err != nil {
				return "", fmt.Errorf("append raw provider response item: %w", err)
			}
		}
		if len(response.ToolCalls) != 0 {
			seenCallIDs := make(map[string]struct{}, len(response.ToolCalls))
			for _, call := range response.ToolCalls {
				if call.ID == "" || call.Name == "" || !json.Valid(call.Arguments) {
					return "", ErrInvalidTurnResponse
				}
				if _, exists := seenCallIDs[call.ID]; exists {
					return "", ErrInvalidTurnResponse
				}
				seenCallIDs[call.ID] = struct{}{}
			}
			outputs, err := r.options.Tools.Execute(ctx, response.ToolCalls)
			if err != nil {
				return "", fmt.Errorf("execute shaper tools: %w", err)
			}
			if len(outputs) != len(response.ToolCalls) {
				return "", ErrInvalidToolResult
			}
			for index, call := range response.ToolCalls {
				if call.ID == "" || outputs[index].CallID != call.ID {
					return "", ErrInvalidToolResult
				}
				if err := conversation.AppendToolOutput(call.ID, outputs[index].Output); err != nil {
					return "", fmt.Errorf("append shaper tool result: %w", err)
				}
			}
			continue
		}
		missing, err := missingRequiredFiles(checkoutRoot, intentPath, acceptancePath)
		if err != nil {
			return "", fmt.Errorf("inspect Shape output paths: %w", err)
		}
		if len(missing) != 0 {
			if err := r.session.RecordFinishGuard(); err != nil {
				return "", err
			}
			if err := conversation.AppendControllerMessage(prompts.FinishGuardMessage(missing)); err != nil {
				return "", fmt.Errorf("append Shape finish guard: %w", err)
			}
			continue
		}
		return response.Text, nil
	}
}

func (r *Runner) finishCandidateFailure(pass int, role contract.RoleName, intentPath, acceptancePath string, candidate *validate.Failure, repairKind shapesession.RepairKind) error {
	if candidate == nil {
		return failure("controller", "shape_validation_failed", "candidate failure was empty", 70, ErrValidationNotComplete)
	}
	feedback := candidate.Error()
	if err := r.session.SetLastFailure(feedback); err != nil {
		return r.classifyProviderOrController(err)
	}
	if err := r.session.CountValidationPass(); err != nil {
		return r.classifyProviderOrController(err)
	}
	r.emitProgress(fmt.Sprintf("shaper pass=%d role=%s validation_failed reason=%s", pass, role, candidate.Reason))
	if pass >= shapesession.MaxConversations*shapesession.MaxPassesPerConversation {
		if err := r.session.CompletePass(); err != nil {
			return r.classifyProviderOrController(err)
		}
		return r.repairLimitFailure(candidate)
	}
	if err := r.session.RecordRepair(repairKind); err != nil {
		return r.classifyProviderOrController(err)
	}
	if err := r.session.CompletePass(); err != nil {
		return r.classifyProviderOrController(err)
	}
	readableIntent, readableAcceptance := r.requiredPathStates(intentPath, acceptancePath)
	message := prompts.RepairMessage(readableIntent, readableAcceptance, feedback)
	if err := r.session.Session().AppendControllerMessage(message); err != nil {
		return failure("environment", "shape_output_unavailable", "cannot append validation repair feedback", 3, err)
	}
	return nil
}

func (r *Runner) successResult(intentPath, acceptancePath string, warnings []validate.Warning) (Result, error) {
	receipt, err := r.session.Accounting(shapesession.OutcomeSuccess)
	if err != nil {
		return Result{}, err
	}
	calls := make([]ModelCall, len(r.calls))
	for index, call := range r.calls {
		calls[index] = cloneCall(call)
	}
	return Result{
		IntentPath:     filepath.Join(r.options.Checkout, filepath.FromSlash(intentPath)),
		AcceptancePath: filepath.Join(r.options.Checkout, filepath.FromSlash(acceptancePath)),
		Rounds:         receipt.ValidationPasses,
		Warnings:       cloneWarnings(warnings),
		Calls:          calls,
		TranscriptPath: filepath.Join(r.options.ScratchDir, TranscriptFileName),
	}, nil
}

func (r *Runner) publishAuditArtifacts(root contract.RootedFS, intentPath string, warnings []audit.Warning, result audit.PassResult) error {
	ledger, err := result.LedgerBytes()
	if err != nil {
		return err
	}
	warningBytes, err := result.WarningsBytes(warnings)
	if err != nil {
		return err
	}
	directory := path.Dir(intentPath)
	if err := root.Publish(path.Join(directory, audit.LedgerFileName), ledger, 0o644, contract.PublicationReplace); err != nil {
		return err
	}
	return root.Publish(path.Join(directory, audit.WarningsFileName), warningBytes, 0o644, contract.PublicationReplace)
}

func (r *Runner) auditWarnings(validationWarnings []validate.Warning, auditWarnings []audit.Warning) []audit.Warning {
	return audit.MergeWarnings(append(convertValidationWarnings(validationWarnings), auditWarnings...))
}

func (r *Runner) requiredPathStates(intentPath, acceptancePath string) (prompts.RequiredPath, prompts.RequiredPath) {
	intentState := prompts.RequiredPath{Path: intentPath}
	acceptanceState := prompts.RequiredPath{Path: acceptancePath}
	root, err := r.openRoot(r.options.Checkout)
	if err != nil {
		return intentState, acceptanceState
	}
	defer closeRoot(root)
	intentState.Readable = readableFile(root, intentPath)
	acceptanceState.Readable = readableFile(root, acceptancePath)
	return intentState, acceptanceState
}

func (r *Runner) recordConcerns(text string, seen map[string]struct{}, warnings *[]validate.Warning) {
	inConcerns := false
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "Concerns:" {
			inConcerns = true
			continue
		}
		if !inConcerns {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		concern := strings.TrimPrefix(trimmed, "- ")
		if concern == "" {
			continue
		}
		if _, ok := seen[concern]; ok {
			continue
		}
		seen[concern] = struct{}{}
		*warnings = appendWarnings(*warnings, validate.Warning{Code: "feasibility_concern", ItemIDs: []string{}, Message: concern})
	}
}

func (r *Runner) recordCalls(identity contract.ConversationIdentity, calls []ModelCall) error {
	for _, call := range calls {
		if call.Role == "" {
			call.Role = identity.Role
		}
		if call.Settings.Provider == "" {
			call.Settings = contract.RoleSettings{Provider: identity.Provider, Model: identity.Model, Effort: identity.Effort}
		}
		if call.Role != identity.Role || call.Settings.Provider != identity.Provider || call.Settings.Model != identity.Model || call.Settings.Effort != identity.Effort {
			return ErrRoleManifestMismatch
		}
		if len(call.Requests) == 0 {
			return ErrMissingCallTelemetry
		}
		for _, request := range call.Requests {
			if request.CacheKey != identity.CacheKey || request.ThreadID != identity.ThreadID || request.Role != identity.Role ||
				request.Provider != identity.Provider || request.Model != identity.Model || request.Effort != identity.Effort ||
				!safeEndpointIdentity(request.EndpointHost, request.EndpointPath) || request.BodyBytes <= 0 ||
				request.RequestedAt.IsZero() || request.RespondedAt.IsZero() || request.RespondedAt.Before(request.RequestedAt) ||
				len(request.PrefixSHA256) == 0 {
				return ErrRoleManifestMismatch
			}
		}
		if err := r.appendTranscript(call); err != nil {
			return failure("environment", "shape_scratch_unavailable", "cannot append Shape transcript", 3, err)
		}
		r.calls = append(r.calls, cloneCall(call))
	}
	return nil
}

func safeEndpointIdentity(host, endpointPath string) bool {
	if host == "" || endpointPath == "" || !strings.HasPrefix(endpointPath, "/") {
		return false
	}
	if strings.ContainsAny(host, "/?#@ \t\r\n") || strings.ContainsAny(endpointPath, "?#\r\n") {
		return false
	}
	return true
}

func (r *Runner) appendTranscript(call ModelCall) error {
	row := transcriptCall{
		Kind: "model_call", Role: call.Role, Provider: call.Settings.Provider,
		Model: call.Settings.Model, Effort: call.Settings.Effort,
		Usage: cloneUsage(call.Usage), WallMS: call.WallMS,
		Requests: make([]requestRecord, len(call.Requests)),
	}
	for index, request := range call.Requests {
		row.Requests[index] = requestRecord{
			EndpointHost: request.EndpointHost, EndpointPath: request.EndpointPath,
			RequestedAt: request.RequestedAt, RespondedAt: request.RespondedAt,
			BodyBytes: request.BodyBytes, PrefixSHA256: append([]string(nil), request.PrefixSHA256...),
			CacheKey: request.CacheKey, ThreadID: request.ThreadID, Role: request.Role,
			Provider: request.Provider, Model: request.Model, Effort: request.Effort,
			RoutingHeaderNames:    append([]string(nil), request.RoutingHeaderNames...),
			CodexTurnStatePresent: request.CodexTurnStatePresent, Usage: cloneUsage(request.Usage),
		}
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return r.options.ScratchRoot.Append(TranscriptFileName, encoded, 0o600)
}

func (r *Runner) conversationTurns(index int) uint64 {
	receipt, err := r.session.Accounting(shapesession.OutcomeFailure)
	if err != nil || index < 1 || index > len(receipt.Conversations) {
		return 0
	}
	return receipt.Conversations[index-1].LogicalTurns
}

func (r *Runner) exhaustionFailure(err error) error {
	var exhausted *shapesession.BudgetExhaustedError
	if !errors.As(err, &exhausted) {
		return r.classifyProviderOrController(err)
	}
	if exhausted.Conversation == 2 && exhausted.Counter == shapesession.TurnBudget {
		return failure("candidate", "shape_turn_limit", "Shaper exhausted its turn limit.", 1, err)
	}
	return r.repairLimitFailureFromLastFailure(err)
}

func (r *Runner) repairLimitFailure(candidate *validate.Failure) error {
	message := fmt.Sprintf("Shaper repair limit reached for %s after %d pass(es) and %d model call(s).\n\n%s",
		candidate.Reason, r.totalPasses(), r.totalShaperTurns(), candidate.Error())
	return failure("candidate", candidate.Reason, message, 1, nil)
}

func (r *Runner) repairLimitFailureFromLastFailure(cause error) error {
	last := r.session.LastFailure()
	if last == "" {
		return failure("candidate", "shape_turn_limit", "Shaper exhausted its turn limit.", 1, cause)
	}
	reason := "shape_repair_limit"
	if split := strings.Index(last, "/"); split >= 0 {
		if colon := strings.IndexByte(last[split+1:], ':'); colon >= 0 {
			reason = last[split+1 : split+1+colon]
		}
	}
	detail := fmt.Sprintf("Shaper repair limit reached for %s after %d pass(es) and %d model call(s).\n\n%s",
		reason, r.totalPasses(), r.totalShaperTurns(), last)
	return failure("candidate", reason, detail, 1, cause)
}

func (r *Runner) totalPasses() uint64 {
	receipt, err := r.session.Accounting(shapesession.OutcomeFailure)
	if err != nil {
		return 0
	}
	return receipt.ValidationPasses
}

func (r *Runner) totalShaperTurns() uint64 {
	receipt, err := r.session.Accounting(shapesession.OutcomeFailure)
	if err != nil {
		return 0
	}
	var total uint64
	for _, role := range receipt.Roles {
		if role.AssignedRole == "shaper" || role.AssignedRole == "fallback_shaper" {
			total += role.LogicalTurns
		}
	}
	return total
}

func (r *Runner) validationConstructionFailure(err error) error {
	if strings.Contains(err.Error(), "invalid Intent slug") {
		return failure("intent", "invalid_slug", "Slug must use lowercase letters, digits, and dashes.", 2, err)
	}
	return failure("environment", "shape_output_unavailable", "cannot initialize Shape validation", 3, err)
}

func (r *Runner) classifyProviderOrController(err error) error {
	if err == nil {
		return nil
	}
	var failureValue *contract.Failure
	if errors.As(err, &failureValue) {
		return failureValue
	}
	var retryFailure interface {
		error
		RetryClass() string
	}
	if errors.As(err, &retryFailure) {
		class := retryFailure.RetryClass()
		if class == "" {
			class = "provider_failed"
		}
		return failure("provider", class, err.Error(), 4, err)
	}
	if errors.Is(err, context.Canceled) {
		return failure("controller", "cancelled", "Shape was interrupted", 130, err)
	}
	return failure("controller", "internal_error", "Shape operation failed", 70, err)
}

func classifyEffectError(err error, reason string, exit int) error {
	if err == nil {
		return nil
	}
	var failureValue *contract.Failure
	if errors.As(err, &failureValue) {
		return failureValue
	}
	if strings.Contains(err.Error(), "environment/") {
		line := err.Error()
		return &contract.Failure{Class: "environment", Reason: contract.ErrorReason(reason), Exit: contract.ExitCode(exit), Message: line, Cause: err}
	}
	return failure("environment", reason, err.Error(), exit, err)
}

func failure(class, reason, detail string, exit int, cause error) *contract.Failure {
	message := class + "/" + reason
	if detail != "" {
		message += ": " + detail
	}
	return &contract.Failure{Class: contract.ErrorClass(class), Reason: contract.ErrorReason(reason), Exit: contract.ExitCode(exit), Message: message, Cause: cause}
}

func (r *Runner) emitProgress(line string) {
	if r.options.Progress != nil {
		r.options.Progress(line)
	}
}

func (r *Runner) emitFallbackStarted(pass int) {
	if !r.session.FallbackStarted() || r.fallbackEventWritten {
		return
	}
	r.fallbackEventWritten = true
	if pass < 4 {
		pass = 4
	}
	r.emitProgress(fmt.Sprintf("shaper pass=%d role=fallback_shaper fallback_started", pass))
}

func (r *Runner) openRoot(directory string) (contract.RootedFS, error) {
	roots := r.options.Roots
	if roots == nil {
		roots = safefs.Opener{}
	}
	return roots.OpenRoot(directory)
}

func closeRoot(root contract.RootedFS) {
	if closer, ok := root.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func cleanAbsolute(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func missingRequiredFiles(root contract.RootedFS, required ...string) ([]string, error) {
	missing := make([]string, 0, len(required))
	for _, name := range required {
		if readableFile(root, name) {
			continue
		}
		missing = append(missing, name)
	}
	if root == nil {
		return nil, safefs.ErrUnsafePath
	}
	return missing, nil
}

func readableFile(root contract.RootedFS, name string) bool {
	if root == nil || !fs.ValidPath(name) || name == "." {
		return false
	}
	_, err := root.ReadFile(name)
	return err == nil
}

func passForFallback() int { return shapesession.MaxPassesPerConversation + 1 }

func styleRepairMessage(issues []intent.LintIssue) string {
	lines := make([]string, 0, len(issues))
	for _, issue := range issues {
		line := issue.Rule
		if issue.Line != nil {
			line += fmt.Sprintf(" at line %d", *issue.Line)
		}
		line += ": " + issue.Message
		lines = append(lines, "- "+line)
	}
	return "Repair these style findings without changing the Request or weakening the Acceptance items:\n" + strings.Join(lines, "\n")
}

func appendWarnings(current []validate.Warning, additions ...validate.Warning) []validate.Warning {
	all := append(cloneWarnings(current), additions...)
	seen := make(map[string]struct{}, len(all))
	result := make([]validate.Warning, 0, len(all))
	for _, warning := range all {
		if warning.ItemIDs == nil {
			warning.ItemIDs = []string{}
		}
		encoded, _ := json.Marshal(warning)
		if _, ok := seen[string(encoded)]; ok {
			continue
		}
		seen[string(encoded)] = struct{}{}
		warning.ItemIDs = append([]string(nil), warning.ItemIDs...)
		result = append(result, warning)
	}
	return result
}

func hasLintWarning(warnings []validate.Warning) bool {
	for _, warning := range warnings {
		if strings.HasPrefix(warning.Code, "lint_") {
			return true
		}
	}
	return false
}

func auditResponseFailure(err error) *validate.Failure {
	if err == nil {
		return nil
	}
	detail := err.Error()
	for _, marker := range []string{
		"requirement auditor returned invalid JSON",
		"requirement auditor reply must contain rows",
		"requirement auditor returned a ledger row missing constraint or maps_to",
	} {
		if strings.Contains(detail, marker) {
			return &validate.Failure{Reason: "requirement_audit_failed", Detail: detail}
		}
	}
	for _, marker := range []string{
		"acceptance test auditor returned invalid JSON",
		"acceptance test auditor reply must contain items",
		"acceptance test auditor returned an item missing id or verdict",
	} {
		if strings.Contains(detail, marker) {
			return &validate.Failure{Reason: "test_audit_failed", Detail: detail}
		}
	}
	return nil
}

func mergeWarnings(current []validate.Warning, additions ...validate.Warning) []validate.Warning {
	return appendWarnings(current, additions...)
}

func cloneWarnings(warnings []validate.Warning) []validate.Warning {
	result := make([]validate.Warning, len(warnings))
	for index, warning := range warnings {
		result[index] = validate.Warning{Code: warning.Code, ItemIDs: append([]string(nil), warning.ItemIDs...), Message: warning.Message}
	}
	return result
}

func convertValidationWarnings(warnings []validate.Warning) []audit.Warning {
	result := make([]audit.Warning, len(warnings))
	for index, warning := range warnings {
		result[index] = audit.Warning{Code: warning.Code, ItemIDs: append([]string(nil), warning.ItemIDs...), Message: warning.Message}
	}
	return result
}

func convertAuditWarnings(warnings []audit.Warning) []validate.Warning {
	result := make([]validate.Warning, len(warnings))
	for index, warning := range warnings {
		result[index] = validate.Warning{Code: warning.Code, ItemIDs: append([]string(nil), warning.ItemIDs...), Message: warning.Message}
	}
	return result
}

func cloneCall(call ModelCall) ModelCall {
	call.Usage = cloneUsage(call.Usage)
	call.Requests = append([]contract.RequestEvidence(nil), call.Requests...)
	for index := range call.Requests {
		call.Requests[index].PrefixSHA256 = append([]string(nil), call.Requests[index].PrefixSHA256...)
		call.Requests[index].RoutingHeaderNames = append([]string(nil), call.Requests[index].RoutingHeaderNames...)
		call.Requests[index].Usage = cloneUsage(call.Requests[index].Usage)
	}
	return call
}

func cloneUsage(usage *contract.TokenUsage) *contract.TokenUsage {
	if usage == nil {
		return nil
	}
	clone := *usage
	clone.Input = cloneInt(usage.Input)
	clone.CachedInput = cloneInt(usage.CachedInput)
	clone.CacheWrite = cloneInt(usage.CacheWrite)
	clone.Output = cloneInt(usage.Output)
	clone.Reasoning = cloneInt(usage.Reasoning)
	return &clone
}

func cloneInt(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

type captureAdapter struct {
	delegate validate.Adapter
	results  map[string]bool
	paths    validate.Paths
	pathsSet bool
}

func (a *captureAdapter) Paths(slug string) (validate.Paths, error) {
	paths, err := a.delegate.Paths(slug)
	if err == nil {
		a.paths = paths
		a.pathsSet = true
	}
	return paths, err
}

func (a *captureAdapter) BaseResults(ctx context.Context, checkout, source string, ids []string) (map[string]bool, error) {
	results, err := a.delegate.BaseResults(ctx, checkout, source, ids)
	if err == nil {
		a.results = cloneResultMap(results)
	}
	return results, err
}

func (a *captureAdapter) Results() map[string]bool { return cloneResultMap(a.results) }

func (a *captureAdapter) PathsSnapshot() validate.Paths {
	if !a.pathsSet {
		return validate.Paths{}
	}
	return a.paths
}

func cloneResultMap(results map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(results))
	for id, passed := range results {
		clone[id] = passed
	}
	return clone
}

type attemptMeter struct {
	session     *shapesession.Run
	identity    contract.ConversationIdentity
	turnOpen    bool
	attemptOpen bool
	started     bool
	attempts    int
	firstError  error
	requests    []contract.RequestEvidence
	usage       *contract.TokenUsage
}

func (m *attemptMeter) Dispatch(kind shapesession.AttemptKind) error {
	if m == nil || m.session == nil {
		return ErrInvalidOptions
	}
	if m.firstError != nil {
		return m.firstError
	}
	if m.attemptOpen || (m.started && kind == shapesession.AttemptFirst) || (!m.started && kind != shapesession.AttemptFirst) {
		m.firstError = shapesession.ErrInvalidAttempt
		return m.firstError
	}
	if err := m.session.DispatchShaperAttempt(kind); err != nil {
		m.firstError = err
		return err
	}
	m.started = true
	m.turnOpen = true
	m.attemptOpen = true
	m.attempts++
	return nil
}

func (m *attemptMeter) RecordRequest(evidence contract.RequestEvidence) error {
	if m == nil || !m.attemptOpen {
		return shapesession.ErrInvalidAttempt
	}
	if evidence.CacheKey != m.identity.CacheKey || evidence.ThreadID != m.identity.ThreadID || evidence.Role != m.identity.Role ||
		evidence.Provider != m.identity.Provider || evidence.Model != m.identity.Model || evidence.Effort != m.identity.Effort ||
		evidence.EndpointHost == "" || evidence.EndpointPath == "" || evidence.BodyBytes < 0 {
		m.firstError = ErrRoleManifestMismatch
		return m.firstError
	}
	m.requests = append(m.requests, cloneEvidence(evidence))
	return nil
}

func (m *attemptMeter) Complete(usage *contract.TokenUsage) error {
	if m == nil || m.session == nil || !m.attemptOpen {
		return shapesession.ErrInvalidAttempt
	}
	if err := m.session.CompleteShaperAttempt(usage); err != nil {
		m.firstError = errors.Join(m.firstError, err)
		return err
	}
	m.attemptOpen = false
	if err := addCallUsage(&m.usage, usage); err != nil {
		m.firstError = errors.Join(m.firstError, err)
		return err
	}
	return nil
}

func (m *attemptMeter) finish() error {
	if m == nil {
		return ErrInvalidOptions
	}
	var finishErr error
	if m.attemptOpen {
		// Dispatch is the accounting boundary. A transport that failed to return
		// nullable usage still counts as one unknown-usage attempt.
		finishErr = m.Complete(nil)
	}
	if m.turnOpen {
		if err := m.session.EndShaperTurn(); err != nil {
			finishErr = errors.Join(finishErr, err)
		} else {
			m.turnOpen = false
		}
	}
	if m.started && m.attempts == 0 {
		finishErr = errors.Join(finishErr, ErrNoRequestDispatched)
	}
	return errors.Join(m.firstError, finishErr)
}

func (m *attemptMeter) normalizeCalls(calls []ModelCall) ([]ModelCall, error) {
	if len(calls) > 1 {
		return nil, ErrRoleManifestMismatch
	}
	if len(calls) == 0 && len(m.requests) == 0 {
		return nil, nil
	}
	if len(calls) == 0 {
		calls = []ModelCall{{Role: m.identity.Role, Settings: contract.RoleSettings{Provider: m.identity.Provider, Model: m.identity.Model, Effort: m.identity.Effort}}}
	}
	call := calls[0]
	if call.Role == "" {
		call.Role = m.identity.Role
	}
	if call.Settings.Provider == "" {
		call.Settings = contract.RoleSettings{Provider: m.identity.Provider, Model: m.identity.Model, Effort: m.identity.Effort}
	}
	if len(call.Requests) == 0 {
		call.Requests = append([]contract.RequestEvidence(nil), m.requests...)
	}
	if m.usage != nil {
		call.Usage = cloneUsage(m.usage)
	}
	if call.WallMS == 0 {
		for _, request := range call.Requests {
			if request.RespondedAt.After(request.RequestedAt) {
				elapsed := uint64(request.RespondedAt.Sub(request.RequestedAt).Milliseconds())
				if elapsed > call.WallMS {
					call.WallMS = elapsed
				}
			}
		}
	}
	if m.attempts != 0 && len(call.Requests) != 0 && len(call.Requests) != m.attempts {
		return nil, ErrMissingCallTelemetry
	}
	return []ModelCall{call}, nil
}

func cloneEvidence(evidence contract.RequestEvidence) contract.RequestEvidence {
	evidence.PrefixSHA256 = append([]string(nil), evidence.PrefixSHA256...)
	evidence.RoutingHeaderNames = append([]string(nil), evidence.RoutingHeaderNames...)
	evidence.Usage = cloneUsage(evidence.Usage)
	return evidence
}

func addCallUsage(target **contract.TokenUsage, usage *contract.TokenUsage) error {
	if usage == nil {
		return nil
	}
	if *target == nil {
		*target = &contract.TokenUsage{}
	}
	for _, values := range []struct {
		destination **int64
		value       *int64
	}{
		{&(*target).Input, usage.Input},
		{&(*target).CachedInput, usage.CachedInput},
		{&(*target).CacheWrite, usage.CacheWrite},
		{&(*target).Output, usage.Output},
		{&(*target).Reasoning, usage.Reasoning},
	} {
		if values.value == nil {
			continue
		}
		if *values.destination == nil {
			value := *values.value
			*values.destination = &value
			continue
		}
		if *values.value > 0 && **values.destination > int64(^uint64(0)>>1)-*values.value {
			return errors.New("shape run: model-call token count overflow")
		}
		if *values.value < 0 && **values.destination < -int64(^uint64(0)>>1)-1-*values.value {
			return errors.New("shape run: model-call token count overflow")
		}
		**values.destination += *values.value
	}
	return nil
}

type transcriptCall struct {
	Kind     string               `json:"kind"`
	Role     contract.RoleName    `json:"role"`
	Provider string               `json:"provider"`
	Model    string               `json:"model"`
	Effort   string               `json:"effort"`
	Usage    *contract.TokenUsage `json:"usage"`
	WallMS   uint64               `json:"wall_ms"`
	Requests []requestRecord      `json:"requests"`
}

// requestRecord explicitly serializes the safe transport observations; it has
// no credential, header-value, prompt, body, or opaque turn-state field.
type requestRecord struct {
	EndpointHost          string               `json:"endpoint_host"`
	EndpointPath          string               `json:"endpoint_path"`
	RequestedAt           time.Time            `json:"requested_at"`
	RespondedAt           time.Time            `json:"responded_at"`
	BodyBytes             int64                `json:"body_bytes"`
	PrefixSHA256          []string             `json:"prefix_sha256"`
	CacheKey              string               `json:"cache_key"`
	ThreadID              string               `json:"thread_id"`
	Role                  contract.RoleName    `json:"role"`
	Provider              string               `json:"provider"`
	Model                 string               `json:"model"`
	Effort                string               `json:"effort"`
	RoutingHeaderNames    []string             `json:"routing_header_names"`
	CodexTurnStatePresent bool                 `json:"codex_turn_state_present"`
	Usage                 *contract.TokenUsage `json:"usage"`
}
