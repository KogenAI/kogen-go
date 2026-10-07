// Package contract defines the language-neutral records and effect ports shared
// by the Go implementation. It contains no command routing or policy engine.
package contract

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// ErrorClass and ErrorReason are strings so the policy owners can bind their
// values to the frozen spec without forcing unrelated components to import one
// another.
type ErrorClass string
type ErrorReason string
type ExitCode int

// RoleName identifies a configured provider/model role. fallback_shaper is an
// effective alias, not a caller-configurable role key.
type RoleName string

// RoleSettings is the provider/model/effort tuple resolved for one role.
type RoleSettings struct {
	Provider string
	Model    string
	Effort   string
}

// RoleManifest is the effective role set after project → machine → default
// merging. FallbackShaper is derived from the effective shaper settings.
type RoleManifest struct {
	Effective      map[RoleName]RoleSettings
	FallbackShaper RoleSettings
}

// Failure is the typed boundary error used by command and adapter layers.
// Public text and exit semantics are selected by the owning command contract.
type Failure struct {
	Class   ErrorClass
	Reason  ErrorReason
	Exit    ExitCode
	Message string
	Cause   error
}

func (f *Failure) Error() string {
	if f == nil {
		return "<nil>"
	}
	if f.Message != "" {
		return f.Message
	}
	if f.Class != "" && f.Reason != "" {
		return string(f.Class) + "/" + string(f.Reason)
	}
	if f.Class != "" {
		return string(f.Class)
	}
	if f.Reason != "" {
		return string(f.Reason)
	}
	if f.Cause != nil {
		return f.Cause.Error()
	}
	return "operation failed"
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

// ProcessSpec describes one supervised child invocation. Env is the complete
// child environment when non-nil; Args and Stdin contain data, never shell code.
type ProcessSpec struct {
	Executable      string
	Args            []string
	Dir             string
	Env             []string
	Stdin           []byte
	Timeout         time.Duration
	OutputLimit     int64
	OutputTailLimit int
	LogPath         string
}

// ProcessResult reports observed child effects. A missing executable is
// represented as unavailable, consistent with the process-custody spec.
type ProcessResult struct {
	ExitStatus  *int
	TimedOut    bool
	Unavailable bool
	OutputTail  []byte
	LogPath     string
	Duration    time.Duration
}

// ProcessRunner supervises process groups, deadlines, termination/reaping and
// bounded output capture for every child process.
type ProcessRunner interface {
	Run(context.Context, ProcessSpec) (ProcessResult, error)
}

// GitPolicy contains trusted invocation controls. Production policy owns origin
// identity/signing and excludes workspace-selected helpers, filters, hooks and
// global configuration; hermetic test policy is built only by testkit.
type GitPolicy struct {
	WorkingDirectory string
	Environment      []string
	Timeout          time.Duration
	StdoutLimit      int64
	StderrTailLimit  int
}

// GitResult preserves command output and process metadata without deciding
// whether a Git exit status is acceptable for the caller's operation.
type GitResult struct {
	Process    ProcessResult
	Stdout     []byte
	StderrTail []byte
}

// GitPort is the only controller-facing Git invocation seam. Callers pass
// messages and large path lists through stdin, not oversized argv elements.
type GitPort interface {
	Exec(context.Context, []string, []byte, GitPolicy) (GitResult, error)
}

// Clock makes deadlines and waits deterministic in tests. Sleep must return
// promptly when ctx is cancelled.
type Clock interface {
	Now() time.Time
	Sleep(context.Context, time.Duration) error
}

// JitterSource returns a uniformly selected value in [0, upperExclusive).
// It is injected so retry decisions can be reproduced without copying policy.
type JitterSource interface {
	Uint64n(upperExclusive uint64) (uint64, error)
}

// PublicationMode controls whether a publication replaces an existing leaf or
// succeeds only when the leaf is absent.
type PublicationMode uint8

const (
	PublicationReplace PublicationMode = iota + 1
	PublicationCreateOnly
)

// RootedFS exposes only root-relative filesystem operations. Implementations
// must validate fs.ValidPath names, anchor operations to descriptors, avoid
// parent-symlink races, refuse FIFO/device opens and hardlink alias writes, and
// publish via fsync plus atomic replacement and parent-directory fsync. Replace
// removes/replaces a symlink leaf itself instead of opening its target.
type RootedFS interface {
	OpenRead(string) (io.ReadCloser, error)
	ReadFile(string) ([]byte, error)
	ReadDir(string) ([]fs.DirEntry, error)
	Lstat(string) (fs.FileInfo, error)
	Readlink(string) (string, error)
	MkdirAll(string, fs.FileMode) error
	Publish(string, []byte, fs.FileMode, PublicationMode) error
	Append(string, []byte, fs.FileMode) error
	Remove(string) error
	Rename(string, string) error
	Symlink(string, string) error
	SyncDir(string) error
}

// RootOpener opens an implementation-owned root. A Go os.Root implementation
// still needs no-follow leaf and hardlink checks; it is not an OS sandbox.
type RootOpener interface {
	OpenRoot(string) (RootedFS, error)
}

// FindingIdentity is the stable GNU-style identity used by verification
// receipts. The parser/adapter owner defines canonical rule and symbol values.
type FindingIdentity struct {
	Path   string `json:"path"`
	Rule   string `json:"rule"`
	Symbol string `json:"symbol"`
}

// CheckStatus describes the observed outcome of one configured check.
type CheckStatus string

const (
	CheckGreen       CheckStatus = "green"
	CheckRed         CheckStatus = "red"
	CheckUnavailable CheckStatus = "unavailable"
	CheckTimeout     CheckStatus = "timeout"
	CheckMutating    CheckStatus = "mutating"
)

// CheckSpec is the normalized, ordered invocation for one acceptance item.
type CheckSpec struct {
	Name    string
	Adapter string
	Program string
	Args    []string
	Env     []string
	Timeout time.Duration
}

// CheckResult records an effectful check run. It does not decide whether a
// base failure is excused or whether the overall candidate is eligible.
type CheckResult struct {
	Name        string
	Status      CheckStatus
	ExitStatus  *int
	TimedOut    bool
	Unavailable bool
	Findings    []FindingIdentity
	OutputTail  []byte
	TreeBefore  string
	TreeAfter   string
}

// AcceptanceAdapter runs a configured check against the requested workspace.
// A non-zero check exit is returned as a CheckResult, not a Go error.
type AcceptanceAdapter interface {
	Run(context.Context, string, CheckSpec) (CheckResult, error)
}

// VerificationReceipt binds observed checks to the exact candidate tree and
// protected manifest. A receipt is evidence, not a policy verdict.
type VerificationReceipt struct {
	BaseTree                string
	CandidateTree           string
	ProtectedManifestSHA256 string
	ApprovalSHA256          string
	Checks                  []CheckResult
	StartedAt               time.Time
	CompletedAt             time.Time
}

// ObjectID is a Git object name. The repository determines whether it uses
// SHA-1 or SHA-256; contract consumers must not assume one length.
type ObjectID string

// RefObservation is a point-in-time Git ref read, not an intended update.
type RefObservation struct {
	Name   string
	Target ObjectID
	Exists bool
}

// RefUpdate is an atomic compare-and-swap request against an observed target.
type RefUpdate struct {
	Name     string
	Expected ObjectID
	Next     ObjectID
}

// RefUpdateResult records the actual compare-and-swap outcome and observed tip.
type RefUpdateResult struct {
	Updated        bool
	ObservedTarget ObjectID
}

// CommitTreeRequest contains exact Git objects and bytes for a landing commit.
// Its implementation applies the user's production identity and signing policy.
type CommitTreeRequest struct {
	Tree    ObjectID
	Parents []ObjectID
	Message []byte
}

// LandingPort exposes real Git effects used by landing and reconciliation.
type LandingPort interface {
	CommitTree(context.Context, CommitTreeRequest) (ObjectID, error)
	ReadRef(context.Context, string) (RefObservation, error)
	CompareAndSwap(context.Context, RefUpdate) (RefUpdateResult, error)
	IsAncestor(context.Context, ObjectID, ObjectID) (bool, error)
}

// RunObservation is read from durable state and process ownership evidence.
type RunObservation struct {
	RunID          string
	ApprovalCommit ObjectID
	Status         string
	Reason         string
	LastEvent      string
	OwnerPID       int
	OwnerAlive     bool
	StartedAt      time.Time
}

// IntentObservation contains the state inputs needed for status and queue
// derivation. Status owners derive policy from these facts rather than trusting
// a precomputed status string from a replay adapter.
type IntentObservation struct {
	Slug           string
	Priority       int
	ApprovalCommit ObjectID
	LandedCommit   ObjectID
	Dependencies   []string
	LatestRun      *RunObservation
}

// StateQuery selects the project rows and refs to observe.
type StateQuery struct {
	ProjectRoot string
	Slugs       []string
}

// StateObservations is an immutable snapshot of observed repository/controller
// facts. It deliberately contains no derived public status or queue ordering.
type StateObservations struct {
	ObservedAt time.Time
	Intents    []IntentObservation
	Refs       []RefObservation
}

// StateObserver reads durable state and live-owner/ref facts for derivation.
type StateObserver interface {
	Observe(context.Context, StateQuery) (StateObservations, error)
}

// TokenUsage keeps missing provider counters null instead of treating them as
// zero. Input excludes cached input where the provider reports both.
type TokenUsage struct {
	Input       *int64 `json:"input,omitempty"`
	CachedInput *int64 `json:"cached_input,omitempty"`
	CacheWrite  *int64 `json:"cache_write,omitempty"`
	Output      *int64 `json:"output,omitempty"`
	Reasoning   *int64 `json:"reasoning,omitempty"`
}

// RequestEvidence is safe telemetry: endpoint identity and routing header names
// are recorded, but credential/header values and prompt/body contents are not.
type RequestEvidence struct {
	EndpointHost          string
	EndpointPath          string
	RequestedAt           time.Time
	RespondedAt           time.Time
	BodyBytes             int64
	PrefixSHA256          []string
	CacheKey              string
	ThreadID              string
	Role                  RoleName
	Provider              string
	Model                 string
	Effort                string
	RoutingHeaderNames    []string
	CodexTurnStatePresent bool
	Usage                 *TokenUsage
}

// ProviderRequest is a complete wire request. Headers are kept only at the
// transport boundary; telemetry records header names and no values.
type ProviderRequest struct {
	Endpoint string
	Headers  http.Header
	Body     []byte
}

// String and GoString intentionally omit header values and request contents.
func (r ProviderRequest) String() string {
	var host, path string
	if endpoint, err := url.Parse(r.Endpoint); err == nil {
		host, path = endpoint.Hostname(), endpoint.EscapedPath()
	}
	return fmt.Sprintf("ProviderRequest{host=%q path=%q header_names=%q body_bytes=%d}", host, path, sortedHeaderNames(r.Headers), len(r.Body))
}

func (r ProviderRequest) GoString() string { return r.String() }

// ProviderResponse returns raw response items and nullable usage counters.
type ProviderResponse struct {
	StatusCode int
	Headers    http.Header
	RawItems   []json.RawMessage
	Usage      *TokenUsage
	Evidence   RequestEvidence
}

// String and GoString omit response headers' values and raw response items.
func (r ProviderResponse) String() string {
	return fmt.Sprintf("ProviderResponse{status=%d header_names=%q raw_items=%d usage_present=%t}", r.StatusCode, sortedHeaderNames(r.Headers), len(r.RawItems), r.Usage != nil)
}

func (r ProviderResponse) GoString() string { return r.String() }

func sortedHeaderNames(headers http.Header) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, http.CanonicalHeaderKey(name))
	}
	sort.Strings(names)
	return names
}

// ProviderPort sends one request using the same mutable session object for all
// turns, finish guards, validation and style repairs in that conversation.
type ProviderPort interface {
	Respond(context.Context, *ConversationSession, ProviderRequest) (ProviderResponse, error)
}
