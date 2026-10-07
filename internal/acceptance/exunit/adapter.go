// Package exunit implements the built-in ExUnit acceptance adapter.
package exunit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"kogen-go/internal/acceptance"
	"kogen-go/internal/acceptance/command"
	"kogen-go/internal/contract"
	"kogen-go/internal/process"
	"kogen-go/internal/safefs"
)

const (
	Extension     = "_test.exs"
	CandidateDir  = "test/acceptance"
	FormatterName = "ledger_formatter.ex"
	AcceptanceLog = "logs/acceptance.log"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

var setupSeeds = [...]string{"deps", "_build"}

var (
	ErrInvalidSlug       = errors.New("exunit: invalid Intent slug")
	ErrInvalidRunDir     = errors.New("exunit: run directory must be a clean real directory")
	ErrRunDirInWorkspace = errors.New("exunit: run directory must be outside the workspace")
	ErrFormatterExists   = errors.New("exunit: ledger formatter already exists with unexpected contents or type")
	ErrInvalidFormatter  = errors.New("exunit: formatter path must be clean, absolute UTF-8")
)

// SetupSeeds returns the workspace directories preserved by the ExUnit setup cache.
func SetupSeeds() []string {
	return append([]string(nil), setupSeeds[:]...)
}

// SourcePath resolves a staged ExUnit test in the checkout.
func SourcePath(slug string) (string, error) {
	if err := validateSlug(slug); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(".kogen", "acceptance", slug+Extension)), nil
}

// CandidatePath resolves the single landed ExUnit test path.
func CandidatePath(slug string) (string, error) {
	if err := validateSlug(slug); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(CandidateDir, slug+Extension)), nil
}

func validateSlug(slug string) error {
	if len(slug) < 3 || len(slug) > 48 || !slugPattern.MatchString(slug) {
		return fmt.Errorf("%w %q", ErrInvalidSlug, slug)
	}
	return nil
}

// FormatterSource returns the generated ExUnit formatter source. It is stored
// in the private run directory, never in the project workspace.
func FormatterSource() string { return formatterSource }

// WriteFormatter publishes the generated formatter into runDir with private
// permissions. An identical existing regular file is safe to reuse; other
// occupants are rejected without following or replacing them.
func WriteFormatter(runDir string, roots contract.RootOpener) (string, error) {
	if !cleanAbsoluteDirectory(runDir) {
		return "", ErrInvalidRunDir
	}
	if roots == nil {
		roots = safefs.Opener{}
	}
	root, err := roots.OpenRoot(runDir)
	if err != nil {
		return "", fmt.Errorf("exunit: open run directory: %w", err)
	}
	defer closeRoot(root)
	info, err := root.Lstat(".")
	if err != nil {
		return "", fmt.Errorf("%w: inspect root: %v", ErrInvalidRunDir, err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: root is not a real directory", ErrInvalidRunDir)
	}

	name := FormatterName
	info, err = root.Lstat(name)
	if err == nil {
		if info.Mode().IsRegular() && info.Mode()&fs.ModeSymlink == 0 && info.Mode().Perm() == 0o600 {
			contents, readErr := root.ReadFile(name)
			if readErr == nil && string(contents) == formatterSource {
				return filepath.Join(runDir, name), nil
			}
		}
		return "", fmt.Errorf("%w: %s", ErrFormatterExists, name)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("exunit: inspect formatter: %w", err)
	}
	if err := root.Publish(name, []byte(formatterSource), 0o600, contract.PublicationCreateOnly); err != nil {
		return "", fmt.Errorf("exunit: publish formatter: %w", err)
	}
	return filepath.Join(runDir, name), nil
}

// RunnerCommand builds the exact ExUnit invocation. The final {path} argument
// is substituted by the shared command adapter with the candidate path.
func RunnerCommand(formatterPath string, useMise bool) ([]string, error) {
	if filepath.Base(formatterPath) != FormatterName || !cleanAbsoluteDirectory(filepath.Dir(formatterPath)) || filepath.Clean(formatterPath) != formatterPath || strings.ContainsRune(formatterPath, '\x00') || !utf8.ValidString(formatterPath) {
		return nil, ErrInvalidFormatter
	}
	command := make([]string, 0, 14)
	if useMise {
		command = append(command, "mise", "exec", "--")
	}
	command = append(command,
		"elixir", "-e", "Code.require_file("+elixirString(formatterPath)+")",
		"-S", "mix", "test", "--formatter", "KogenLedgerFormatter",
		"--formatter", "ExUnit.CLIFormatter", "{path}",
	)
	return command, nil
}

// Formatter derives the shaping formatter for Elixir files. The first check
// containing both `format` and `--check-formatted` wins, with only that flag
// removed; otherwise Mix's formatter is used.
func Formatter(checks []contract.CheckSpec, file string) []string {
	ext := filepath.Ext(file)
	if ext != ".ex" && ext != ".exs" {
		return nil
	}
	for _, check := range checks {
		argv := make([]string, 0, len(check.Args)+1)
		argv = append(argv, check.Program)
		argv = append(argv, check.Args...)
		hasFormat, hasCheck := false, false
		for _, arg := range argv {
			hasFormat = hasFormat || arg == "format"
			hasCheck = hasCheck || arg == "--check-formatted"
		}
		if !hasFormat || !hasCheck {
			continue
		}
		result := make([]string, 0, len(argv)-1)
		for _, arg := range argv {
			if arg != "--check-formatted" {
				result = append(result, arg)
			}
		}
		return result
	}
	return []string{"mix", "format", file}
}

// Unavailable detects missing Erlang/Elixir/Mix runtimes in the first 20 log
// lines. The same words later in normal test output do not make the adapter
// unavailable.
func Unavailable(log []byte) bool {
	lines := strings.Split(string(log), "\n")
	if len(lines) > 20 {
		lines = lines[:20]
	}
	for _, line := range lines {
		if missingRuntimeLine(line) {
			return true
		}
	}
	return false
}

var missingMarkers = [...]string{"not found", "no such file", "could not find", "cannot find", "can't find", "not recognized"}

func missingRuntimeLine(line string) bool {
	lower := strings.ToLower(line)
	missing := false
	for _, marker := range missingMarkers {
		if strings.Contains(lower, marker) {
			missing = true
			break
		}
	}
	if !missing {
		return false
	}
	for _, runtime := range []string{"erl", "elixir", "mix"} {
		for start := 0; start < len(lower); {
			rel := strings.Index(lower[start:], runtime)
			if rel < 0 {
				break
			}
			index := start + rel
			before, after := byte(0), byte(0)
			if index > 0 {
				before = lower[index-1]
			}
			if end := index + len(runtime); end < len(lower) {
				after = lower[end]
			}
			if !identifierByte(before) && !identifierByte(after) {
				return true
			}
			start = index + len(runtime)
		}
	}
	return false
}

func identifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_'
}

// Request contains one ExUnit acceptance invocation.
type Request struct {
	Slug          string
	Workdir       string
	RunDir        string
	ReportPath    string
	Environment   process.Environment
	ExpectedItems []string
	UseMise       bool
}

// Runner executes ExUnit using the shared command adapter and then applies the
// ExUnit-specific missing-runtime log signal.
type Runner struct {
	Processes contract.ProcessRunner
	Trees     acceptance.TreeSnapshotter
	Roots     contract.RootOpener
}

// Run writes the ledger formatter outside the workspace, supervises ExUnit,
// then interprets its JSONL report using the shared acceptance contract.
func (r *Runner) Run(ctx context.Context, request Request) (acceptance.Result, error) {
	if ctx == nil || r == nil || r.Processes == nil || r.Trees == nil {
		return acceptance.Result{}, errors.New("exunit: context, process runner and tree snapshotter are required")
	}
	if _, err := SourcePath(request.Slug); err != nil {
		return acceptance.Result{}, err
	}
	if _, err := CandidatePath(request.Slug); err != nil {
		return acceptance.Result{}, err
	}
	if !cleanAbsoluteDirectory(request.Workdir) || !cleanAbsoluteDirectory(request.RunDir) {
		return acceptance.Result{}, errors.New("exunit: workdir and run directory must be clean absolute paths")
	}
	if runDirectoryWithin(request.Workdir, request.RunDir) {
		return acceptance.Result{}, ErrRunDirInWorkspace
	}
	formatterPath, err := WriteFormatter(request.RunDir, r.Roots)
	if err != nil {
		return acceptance.Result{}, err
	}
	argv, err := RunnerCommand(formatterPath, request.UseMise)
	if err != nil {
		return acceptance.Result{}, err
	}
	base := command.Runner{Processes: r.Processes, Trees: r.Trees, Roots: r.Roots}
	result, err := base.Run(ctx, command.Request{
		Config: command.Config{Extension: Extension, CandidateDir: CandidateDir, Run: argv},
		Slug:   request.Slug, Workdir: request.Workdir, RunDir: request.RunDir,
		ReportPath: request.ReportPath, Environment: request.Environment,
		ExpectedItems: request.ExpectedItems,
	})
	if err != nil {
		return result, err
	}
	log := readAcceptanceLog(request.RunDir, r.Roots)
	if len(log) == 0 {
		log = result.Process.OutputTail
	}
	if Unavailable(log) {
		result.Process.Unavailable = true
		if len(result.Rows) == 0 {
			filtered := result.Failures[:0]
			for _, failure := range result.Failures {
				if failure.Kind != acceptance.FailureAcceptanceCompileFailed && failure.Kind != acceptance.FailureNoTaggedTests && failure.Kind != acceptance.FailureLedgerInvalid {
					filtered = append(filtered, failure)
				}
			}
			result.Failures = filtered
			if !hasFailure(result.Failures, acceptance.FailureToolMissing) {
				result.Failures = append(result.Failures, acceptance.Failure{Kind: acceptance.FailureToolMissing})
			}
		}
	}
	return result, nil
}

func readAcceptanceLog(runDir string, roots contract.RootOpener) []byte {
	if roots == nil {
		roots = safefs.Opener{}
	}
	root, err := roots.OpenRoot(runDir)
	if err != nil {
		return nil
	}
	defer closeRoot(root)
	contents, err := root.ReadFile(AcceptanceLog)
	if err != nil {
		return nil
	}
	return contents
}

func hasFailure(failures []acceptance.Failure, kind acceptance.FailureKind) bool {
	for _, failure := range failures {
		if failure.Kind == kind {
			return true
		}
	}
	return false
}

func cleanAbsoluteDirectory(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func runDirectoryWithin(workdir, runDir string) bool {
	relative, err := filepath.Rel(workdir, runDir)
	return err == nil && (relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) == false)
}

func closeRoot(root contract.RootedFS) {
	if closer, ok := root.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func elixirString(value string) string {
	var result strings.Builder
	result.WriteByte('"')
	runes := []rune(value)
	for index, current := range runes {
		switch current {
		case '\\':
			result.WriteString("\\\\")
		case '"':
			result.WriteString("\\\"")
		case '\n':
			result.WriteString("\\n")
		case '\r':
			result.WriteString("\\r")
		case '\t':
			result.WriteString("\\t")
		case '#':
			if index+1 < len(runes) && runes[index+1] == '{' {
				result.WriteString("\\#")
			} else {
				result.WriteRune(current)
			}
		default:
			result.WriteRune(current)
		}
	}
	result.WriteByte('"')
	return result.String()
}
