package validate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/intent"
	"kogen-go/internal/safefs"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const (
	intentRelativePath = ".kogen/intents"
	ledgerArtifact     = "ledger.json"
	warningsArtifact   = "shape-warnings.json"
)

// Paths are supplied by the selected acceptance adapter. Both paths are
// checkout-relative; their names and suffixes belong to that adapter.
type Paths struct {
	Source    string
	Candidate string
}

// Adapter supplies acceptance-source paths and the adapter-specific base
// result. Implementations may be command, Rails, ExUnit, or another admitted
// adapter. Validation never derives an extension itself.
type Adapter interface {
	Paths(slug string) (Paths, error)
	BaseResults(context.Context, string, string, []string) (map[string]bool, error)
}

// SetupRunner runs the configured, sandboxed and cached project setup.
// Cache identity and process supervision are owned by the setup integration.
type SetupRunner interface {
	Run(context.Context, string) error
}

// Formatter applies the configured formatter to checkout-relative paths.
// unavailable is a warning condition; other errors fail the candidate.
type Formatter interface {
	Format(context.Context, string, []string) (unavailable bool, err error)
}

// AcceptanceChecks runs the configured acceptance_checks against the staged
// candidate path. Process supervision and per-check deadlines belong to the
// implementation of this port.
type AcceptanceChecks interface {
	Run(context.Context, string, string) ([]contract.CheckResult, error)
}

// TreeSnapshotter observes the exact candidate tree around child execution.
type TreeSnapshotter interface {
	Snapshot(context.Context, string) (string, error)
}

// Options contains command-local validation inputs and injected effects.
type Options struct {
	Checkout  string
	Slug      string
	Request   []byte
	GatePaths []string

	Adapter   Adapter
	Setup     SetupRunner
	Formatter Formatter
	Checks    AcceptanceChecks
	Trees     TreeSnapshotter
	Roots     contract.RootOpener
}

// Failure is a candidate validation failure, suitable for appending to the
// shaper repair feedback. Environment and provider failures remain ordinary
// Go errors at their owning boundaries.
type Failure struct {
	Reason string
	Detail string
}

func (f *Failure) Error() string {
	if f == nil {
		return "<nil>"
	}
	if f.Detail == "" {
		return "candidate/" + f.Reason
	}
	return "candidate/" + f.Reason + ": " + f.Detail
}

// Warning is a non-blocking Shape/card warning.
type Warning struct {
	Code    string   `json:"code"`
	ItemIDs []string `json:"item_ids"`
	Message string   `json:"message"`
}

// Outcome is one deterministic validation traversal. A non-nil Failure asks
// for a candidate repair; StyleRepair asks for a free style-only repair. A
// successful result has Validated set and contains the final exact bytes.
type Outcome struct {
	Validated    bool
	StyleRepair  []intent.LintIssue
	Failure      *Failure
	Warnings     []Warning
	Progress     []string
	IntentBytes  []byte
	TestBytes    []byte
	ParsedIntent *intent.Intent
}

// Validator validates generated files using the adapter and effect ports in
// Options. Prepare must run once before Validate to clear stale artifacts and
// perform setup.
type Validator struct {
	checkout   string
	slug       string
	request    []byte
	gatePaths  []string
	intentPath string
	paths      Paths
	adapter    Adapter
	setup      SetupRunner
	formatter  Formatter
	checks     AcceptanceChecks
	trees      TreeSnapshotter
	roots      contract.RootOpener
	prepared   bool
}

// New validates static paths and stores the injected adapter/effect ports.
func New(options Options) (*Validator, error) {
	if options.Checkout == "" || !filepath.IsAbs(options.Checkout) || filepath.Clean(options.Checkout) != options.Checkout || strings.ContainsRune(options.Checkout, '\x00') {
		return nil, errors.New("shape validation: checkout must be a clean absolute directory")
	}
	if !slugPattern.MatchString(options.Slug) || len(options.Slug) < 3 || len(options.Slug) > 48 {
		return nil, errors.New("shape validation: invalid Intent slug")
	}
	if options.Adapter == nil || options.Checks == nil || options.Trees == nil {
		return nil, errors.New("shape validation: acceptance adapter, checks, and tree snapshotter are required")
	}
	paths, err := options.Adapter.Paths(options.Slug)
	if err != nil {
		return nil, fmt.Errorf("shape validation: resolve adapter paths: %w", err)
	}
	if !safeWorktreePath(paths.Source) || !safeWorktreePath(paths.Candidate) || paths.Source == paths.Candidate {
		return nil, errors.New("shape validation: adapter paths must be distinct safe checkout-relative paths")
	}
	if !strings.HasPrefix(paths.Source, ".kogen/acceptance/") {
		return nil, errors.New("shape validation: adapter source must be under .kogen/acceptance")
	}
	if !safeWorktreePath(path.Join(intentRelativePath, options.Slug, "intent.md")) {
		return nil, errors.New("shape validation: invalid Intent path")
	}
	roots := options.Roots
	if roots == nil {
		roots = safefs.Opener{}
	}
	gatePaths := append([]string(nil), options.GatePaths...)
	sort.Slice(gatePaths, func(i, j int) bool { return bytes.Compare([]byte(gatePaths[i]), []byte(gatePaths[j])) < 0 })
	return &Validator{
		checkout:   options.Checkout,
		slug:       options.Slug,
		request:    bytes.Clone(options.Request),
		gatePaths:  gatePaths,
		intentPath: path.Join(intentRelativePath, options.Slug, "intent.md"),
		paths:      paths,
		adapter:    options.Adapter,
		setup:      options.Setup,
		formatter:  options.Formatter,
		checks:     options.Checks,
		trees:      options.Trees,
		roots:      roots,
	}, nil
}

// Prepare removes prior Shape-only reports through a rooted descriptor, then
// invokes the configured setup port. Cleanup errors stop setup from running.
func (v *Validator) Prepare(ctx context.Context) error {
	if ctx == nil || v == nil {
		return errors.New("shape validation: context and validator are required")
	}
	root, err := v.openRoot()
	if err != nil {
		return err
	}
	parentErr := ensureExistingParents(root, path.Join(intentRelativePath, v.slug, ledgerArtifact))
	if parentErr != nil && !errors.Is(parentErr, fs.ErrNotExist) {
		closeRoot(root)
		return fmt.Errorf("shape validation: inspect stale artifacts: %w", parentErr)
	}
	if parentErr == nil {
		for _, artifact := range []string{
			path.Join(intentRelativePath, v.slug, ledgerArtifact),
			path.Join(intentRelativePath, v.slug, warningsArtifact),
		} {
			info, statErr := root.Lstat(artifact)
			if errors.Is(statErr, fs.ErrNotExist) {
				continue
			}
			if statErr != nil {
				closeRoot(root)
				return fmt.Errorf("shape validation: inspect stale artifact: %w", statErr)
			}
			if !info.Mode().IsRegular() && info.Mode()&fs.ModeSymlink == 0 {
				closeRoot(root)
				return fmt.Errorf("shape validation: stale artifact %q is not a regular file or symlink", artifact)
			}
			if removeErr := root.Remove(artifact); removeErr != nil {
				closeRoot(root)
				return fmt.Errorf("shape validation: remove stale artifact %q: %w", artifact, removeErr)
			}
		}
	}
	if err := closeRoot(root); err != nil {
		return fmt.Errorf("shape validation: close checkout root: %w", err)
	}
	if v.setup != nil {
		if err := v.setup.Run(ctx, v.checkout); err != nil {
			return fmt.Errorf("environment/setup_failed: %w", err)
		}
	}
	v.prepared = true
	return nil
}

// Validate normalizes and validates the generated files for one pass. styleRepairs
// is the number already spent in this conversation; values of two or more
// retain remaining style findings as warnings.
func (v *Validator) Validate(ctx context.Context, pass int, role string, styleRepairs int) (Outcome, error) {
	if ctx == nil || v == nil {
		return Outcome{}, errors.New("shape validation: context and validator are required")
	}
	if !v.prepared {
		return Outcome{}, errors.New("shape validation: Prepare must complete before Validate")
	}
	root, err := v.openRoot()
	if err != nil {
		return Outcome{}, err
	}
	defer closeRoot(root)
	generated, generatedMode, err := readRegular(root, v.intentPath)
	if err != nil {
		return Outcome{}, fmt.Errorf("environment/shape_output_unavailable: read Intent: %w", err)
	}
	test, testMode, err := readRegular(root, v.paths.Source)
	if err != nil {
		return Outcome{}, fmt.Errorf("environment/shape_output_unavailable: read acceptance source: %w", err)
	}
	normalized, failure := Normalize(generated, v.request)
	if failure != nil {
		return Outcome{Failure: failure}, nil
	}
	if err := root.Publish(v.intentPath, normalized, generatedMode, contract.PublicationReplace); err != nil {
		return Outcome{}, fmt.Errorf("environment/shape_output_unavailable: normalize Intent: %w", err)
	}
	parsed, failure := parseAndLint(v.slug, normalized)
	if failure != nil {
		return Outcome{Failure: failure, IntentBytes: normalized, TestBytes: test}, nil
	}
	outcome := Outcome{IntentBytes: normalized, TestBytes: test, ParsedIntent: parsed}
	styleIssues := styleIssues(parsed)
	if len(styleIssues) > 0 && styleRepairs < 2 {
		outcome.StyleRepair = styleIssues
		return outcome, nil
	}
	for _, issue := range styleIssues {
		outcome.Warnings = append(outcome.Warnings, styleWarning(parsed, issue))
	}
	if gate := undeclaredGatePath(parsed, test, v.gatePaths); gate != "" {
		outcome.Failure = &Failure{
			Reason: "undeclared_gate_path",
			Detail: fmt.Sprintf("Gate-path edit requires `changes_gate: true`; matched path %s.", gate),
		}
		return outcome, nil
	}
	if v.formatter != nil {
		unavailable, formatErr := v.formatter.Format(ctx, v.checkout, []string{v.intentPath, v.paths.Source})
		if formatErr != nil {
			outcome.Failure = &Failure{Reason: "formatter_failed", Detail: formatErr.Error()}
			return outcome, nil
		}
		if unavailable {
			outcome.Progress = append(outcome.Progress, fmt.Sprintf("shaper pass=%d role=%s warning formatter_unavailable", pass, role))
			outcome.Warnings = append(outcome.Warnings, Warning{Code: "formatter_unavailable", ItemIDs: []string{}, Message: "configured formatter is unavailable"})
		}
	}
	formattedIntent, intentMode, err := readRegular(root, v.intentPath)
	if err != nil {
		return Outcome{}, fmt.Errorf("environment/shape_output_unavailable: read formatted Intent: %w", err)
	}
	formattedTest, testMode, err := readRegular(root, v.paths.Source)
	if err != nil {
		return Outcome{}, fmt.Errorf("environment/shape_output_unavailable: read formatted acceptance source: %w", err)
	}
	parsed, failure = parseAndLint(v.slug, formattedIntent)
	if failure != nil {
		outcome.Failure = failure
		outcome.IntentBytes = formattedIntent
		outcome.TestBytes = formattedTest
		return outcome, nil
	}
	outcome.IntentBytes = formattedIntent
	outcome.TestBytes = formattedTest
	outcome.ParsedIntent = parsed
	itemIDs := make([]string, 0, len(parsed.Acceptance))
	for _, item := range parsed.Acceptance {
		itemIDs = append(itemIDs, item.ID)
	}
	if failure, err := v.stageAndRun(ctx, formattedTest, testMode); err != nil {
		return Outcome{}, err
	} else if failure != nil {
		outcome.Failure = failure
		return outcome, nil
	}
	baseBefore, err := v.trees.Snapshot(ctx, v.checkout)
	if err != nil {
		return Outcome{}, fmt.Errorf("shape validation: snapshot before base acceptance: %w", err)
	}
	baseResults, baseErr := v.adapter.BaseResults(ctx, v.checkout, v.paths.Source, itemIDs)
	baseAfter, snapshotErr := v.trees.Snapshot(ctx, v.checkout)
	if snapshotErr != nil {
		return Outcome{}, errors.Join(baseErr, fmt.Errorf("shape validation: snapshot after base acceptance: %w", snapshotErr))
	}
	baseSource, baseSourceMode, baseSourceErr := readRegular(root, v.paths.Source)
	if baseBefore != baseAfter || baseSourceErr != nil || !bytes.Equal(baseSource, formattedTest) || baseSourceMode.Perm() != testMode.Perm() {
		outcome.Failure = &Failure{Reason: "tree_mutated", Detail: "base acceptance run changed the checkout tree"}
		return outcome, nil
	}
	if baseErr != nil {
		outcome.Failure = &Failure{Reason: "acceptance_failed", Detail: fmt.Sprintf("base acceptance run failed: %v", baseErr)}
		return outcome, nil
	}
	reclassified, reclassWarnings, hasRedChange, failure := Reclassify(v.slug, formattedIntent, baseResults)
	if failure != nil {
		outcome.Failure = failure
		return outcome, nil
	}
	if err := root.Publish(v.intentPath, reclassified, intentMode, contract.PublicationReplace); err != nil {
		return Outcome{}, fmt.Errorf("environment/shape_output_unavailable: publish reclassified Intent: %w", err)
	}
	outcome.IntentBytes = reclassified
	outcome.Warnings = append(outcome.Warnings, reclassWarnings...)
	parsed, failure = parseAndLintAllowNoChange(v.slug, reclassified)
	if failure != nil {
		outcome.Failure = failure
		return outcome, nil
	}
	outcome.ParsedIntent = parsed
	if !hasRedChange {
		outcome.Failure = &Failure{Reason: "all_items_keep", Detail: "at least one test item must be fully red on the base"}
		return outcome, nil
	}
	if _, failure := parseAndLint(v.slug, reclassified); failure != nil {
		outcome.Failure = failure
		return outcome, nil
	}
	outcome.Validated = true
	return outcome, nil
}

func (v *Validator) openRoot() (contract.RootedFS, error) {
	root, err := v.roots.OpenRoot(v.checkout)
	if err != nil {
		return nil, fmt.Errorf("shape validation: open checkout root: %w", err)
	}
	return root, nil
}

func readRegular(root contract.RootedFS, name string) ([]byte, fs.FileMode, error) {
	if err := ensureExistingParents(root, name); err != nil {
		return nil, 0, err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%q is not a regular file", name)
	}
	data, err := root.ReadFile(name)
	if err != nil {
		return nil, 0, err
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	return data, mode, nil
}

func ensureExistingParents(root contract.RootedFS, name string) error {
	if !safeWorktreePath(name) {
		return fmt.Errorf("unsafe checkout-relative path %q", name)
	}
	parent := path.Dir(name)
	if parent == "." {
		return nil
	}
	current := ""
	for _, segment := range strings.Split(parent, "/") {
		if current == "" {
			current = segment
		} else {
			current += "/" + segment
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("parent %q is not a real directory", current)
		}
	}
	return nil
}

func safeWorktreePath(value string) bool {
	if value == "" || strings.ContainsAny(value, "\\\x00\r\n") || path.IsAbs(value) || path.Clean(value) != value || !fs.ValidPath(value) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".git" || segment == ".." {
			return false
		}
	}
	return true
}

func closeRoot(root contract.RootedFS) error {
	if closer, ok := root.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}
