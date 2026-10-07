// Package command implements the project-configured acceptance command
// adapter and its private JSONL report boundary.
package command

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kogen-go/internal/acceptance"
	"kogen-go/internal/contract"
	"kogen-go/internal/process"
	"kogen-go/internal/safefs"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Config is the command-adapter subset of project acceptance configuration.
// Run's first value is the executable; exact {path} substrings are replaced.
type Config struct {
	Extension    string
	CandidateDir string
	Run          []string
	Timeout      time.Duration
}

// Validate checks fields needed by the command adapter. Other stack adapters
// own their own configuration and command construction.
func (c Config) Validate() error {
	if c.Extension == "" || strings.ContainsAny(c.Extension, "/\\\x00\r\n") || c.Extension == "." || c.Extension == ".." {
		return errors.New("acceptance command: ext must be a nonempty filename suffix")
	}
	if !safeRelativeDirectory(c.CandidateDir) {
		return errors.New("acceptance command: candidate_dir must be a safe relative directory")
	}
	if len(c.Run) == 0 || c.Run[0] == "" {
		return errors.New("acceptance command: run must contain a nonempty executable")
	}
	for _, arg := range c.Run {
		if strings.ContainsRune(arg, '\x00') {
			return errors.New("acceptance command: run arguments must not contain NUL")
		}
	}
	if c.Timeout < 0 {
		return errors.New("acceptance command: timeout must not be negative")
	}
	return nil
}

// SourcePath returns the configured acceptance source under .kogen.
func (c Config) SourcePath(slug string) (string, error) {
	if err := c.validateSlug(slug); err != nil {
		return "", err
	}
	if c.Extension == "" || strings.ContainsAny(c.Extension, "/\\\x00\r\n") {
		return "", errors.New("acceptance command: invalid extension")
	}
	return path.Join(".kogen", "acceptance", slug+c.Extension), nil
}

// CandidatePath returns the configured relative candidate path.
func (c Config) CandidatePath(slug string) (string, error) {
	if err := c.validateSlug(slug); err != nil {
		return "", err
	}
	if !safeRelativeDirectory(c.CandidateDir) || c.Extension == "" || strings.ContainsAny(c.Extension, "/\\\x00\r\n") {
		return "", errors.New("acceptance command: invalid candidate path configuration")
	}
	return path.Join(c.CandidateDir, slug+c.Extension), nil
}

func (c Config) validateSlug(slug string) error {
	if !slugPattern.MatchString(slug) || len(slug) < 3 || len(slug) > 48 {
		return fmt.Errorf("acceptance command: invalid Intent slug %q", slug)
	}
	return nil
}

func safeRelativeDirectory(value string) bool {
	if value == "" || strings.ContainsAny(value, "\\\x00\r\n") || path.IsAbs(value) || path.Clean(value) != value || !fs.ValidPath(value) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".git" {
			return false
		}
	}
	return true
}

// Request is one observed command-adapter run. ReportPath defaults to
// <run_dir>/reports/ledger.jsonl. Environment is a complete child environment
// before the adapter's two KOGEN report variables are installed.
type Request struct {
	Config             Config
	Slug               string
	Workdir            string
	RunDir             string
	ReportPath         string
	Environment        process.Environment
	ExpectedItems      []string
	AdapterUnavailable bool
}

// Runner executes command-adapter requests with supervised processes and an
// injected candidate-tree snapshotter.
type Runner struct {
	Processes contract.ProcessRunner
	Trees     acceptance.TreeSnapshotter
	Roots     contract.RootOpener
}

// Run evaluates the ledger produced by the configured runner. It preserves
// the process result and reports ledger, timeout, suite, and tree-mutation
// observations without applying gate or landing policy.
func (r *Runner) Run(ctx context.Context, request Request) (acceptance.Result, error) {
	if ctx == nil || r == nil || r.Processes == nil || r.Trees == nil {
		return acceptance.Result{}, errors.New("acceptance command: context, process runner, and tree snapshotter are required")
	}
	if err := request.Config.Validate(); err != nil {
		return acceptance.Result{}, err
	}
	if err := request.Config.validateSlug(request.Slug); err != nil {
		return acceptance.Result{}, err
	}
	if !cleanAbsoluteDirectory(request.Workdir) || !cleanAbsoluteDirectory(request.RunDir) {
		return acceptance.Result{}, errors.New("acceptance command: workdir and run_dir must be clean absolute directories")
	}
	candidateRelative, err := request.Config.CandidatePath(request.Slug)
	if err != nil {
		return acceptance.Result{}, err
	}
	candidatePath := filepath.Join(request.Workdir, filepath.FromSlash(candidateRelative))
	reportRelative, reportPath, err := validatedReportPath(request.RunDir, request.ReportPath)
	if err != nil {
		return acceptance.Result{}, err
	}
	roots := r.Roots
	if roots == nil {
		roots = safefs.Opener{}
	}
	root, err := roots.OpenRoot(request.RunDir)
	if err != nil {
		return acceptance.Result{}, fmt.Errorf("acceptance command: open run root: %w", err)
	}
	defer closeRooted(root)
	if err := acceptance.EnsurePrivateRunDirectories(root); err != nil {
		return acceptance.Result{}, err
	}
	parent := path.Dir(reportRelative)
	if parent != "." {
		if err := acceptance.EnsurePrivateDirectoryPath(root, parent); err != nil {
			return acceptance.Result{}, err
		}
	}
	if err := clearOldReport(root, reportRelative); err != nil {
		return acceptance.Result{}, err
	}
	before, err := r.Trees.Snapshot(ctx, request.Workdir)
	if err != nil {
		return acceptance.Result{}, fmt.Errorf("acceptance command: snapshot tree before runner: %w", err)
	}
	argv := make([]string, len(request.Config.Run))
	for i, arg := range request.Config.Run {
		argv[i] = strings.ReplaceAll(arg, "{path}", candidatePath)
	}
	environment := make(process.Environment, len(request.Environment)+2)
	for key, value := range request.Environment {
		environment[key] = value
	}
	environment["KOGEN_LEDGER_REPORT"] = reportPath
	environment["KOGEN_INTENT_SLUG"] = request.Slug
	childEnv, err := acceptance.SortedEnvironment(environment)
	if err != nil {
		return acceptance.Result{}, err
	}
	args := append([]string(nil), argv[1:]...)
	timeout := acceptance.NormalizeTimeout(request.Config.Timeout)
	processResult, processErr := r.Processes.Run(ctx, contract.ProcessSpec{
		Executable:      argv[0],
		Args:            args,
		Dir:             request.Workdir,
		Env:             childEnv,
		Timeout:         timeout,
		OutputLimit:     0,
		OutputTailLimit: 0,
		LogPath:         filepath.Join(request.RunDir, "logs", "acceptance.log"),
	})
	after, afterErr := r.Trees.Snapshot(ctx, request.Workdir)
	if afterErr != nil {
		return acceptance.Result{}, errors.Join(processErr, fmt.Errorf("acceptance command: snapshot tree after runner: %w", afterErr))
	}
	report, reportErr := readReport(root, reportRelative)
	result := acceptance.Assess(request.Slug, request.ExpectedItems, processResult, report, reportErr, request.AdapterUnavailable, before != after, before, after)
	return result, processErr
}

func cleanAbsoluteDirectory(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func validatedReportPath(runDir, reportPath string) (string, string, error) {
	if reportPath == "" {
		reportPath = filepath.Join(runDir, "reports", "ledger.jsonl")
	}
	if !cleanAbsoluteDirectory(runDir) || !filepath.IsAbs(reportPath) || filepath.Clean(reportPath) != reportPath {
		return "", "", errors.New("acceptance command: report path must be a clean absolute path")
	}
	relative, err := filepath.Rel(runDir, reportPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("acceptance command: report path must be under run_dir")
	}
	name := filepath.ToSlash(relative)
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		return "", "", errors.New("acceptance command: report path is unsafe")
	}
	return name, reportPath, nil
}

func clearOldReport(root contract.RootedFS, name string) error {
	_, err := root.Lstat(name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("acceptance command: inspect old ledger report: %w", err)
	}
	if err == nil {
		if err := root.Remove(name); err != nil {
			return fmt.Errorf("acceptance command: remove old ledger report: %w", err)
		}
	}
	return nil
}

func readReport(root contract.RootedFS, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &acceptance.LedgerReadError{Kind: acceptance.LedgerMissingReport, Detail: err.Error()}
		}
		return nil, &acceptance.LedgerReadError{Kind: acceptance.LedgerUnsafeReport, Detail: err.Error()}
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, &acceptance.LedgerReadError{Kind: acceptance.LedgerUnsafeReport}
	}
	data, err := root.ReadFile(name)
	if err != nil {
		return nil, &acceptance.LedgerReadError{Kind: acceptance.LedgerUnsafeReport, Detail: err.Error()}
	}
	return data, nil
}

func closeRooted(root contract.RootedFS) {
	if closer, ok := root.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}
