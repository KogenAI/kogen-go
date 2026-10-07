package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/yamlmini"
)

// Issue is one grouped configuration diagnostic. YAML syntax issues carry a
// line; schema diagnostics intentionally do not.
type Issue struct {
	Line   int
	Detail string
}

// ConfigError identifies the file whose YAML or schema was rejected.
type ConfigError struct {
	Path   string
	Issues []Issue
}

func (e *ConfigError) Error() string {
	if e == nil {
		return "project configuration error"
	}
	var b strings.Builder
	b.WriteString(e.Path)
	for _, issue := range e.Issues {
		b.WriteByte('\n')
		if issue.Line > 0 {
			fmt.Fprintf(&b, "line %d: ", issue.Line)
		}
		b.WriteString(issue.Detail)
	}
	return b.String()
}

// Config is a validated .kogen/project.yaml document. Raw preserves typed
// string-valued YAML data for package consumers that need other project fields.
type Config struct {
	Name       string
	Base       string
	Raw        yamlmini.Mapping
	sourcePath string
}

// MachineConfig is the validated top-level build section of ~/.kogen/config.yaml.
type MachineConfig struct {
	Build yamlmini.Mapping
	Raw   yamlmini.Mapping
}

// ParseConfig parses and validates project.yaml, collecting independent schema
// issues into one ConfigError.
func ParseConfig(path string, source []byte) (*Config, error) {
	root, err := parseMap(path, source)
	if err != nil {
		return nil, err
	}
	issues := validateProject(root)
	if len(issues) != 0 {
		return nil, &ConfigError{Path: path, Issues: issues}
	}
	return &Config{Name: scalar(root["name"]), Base: scalar(root["base"]), Raw: root, sourcePath: path}, nil
}

// LoadConfig reads .kogen/project.yaml from checkout. A missing project file
// is represented by a nil config; other read and parse failures are returned.
func LoadConfig(checkout string) (*Config, error) {
	path := filepath.Join(checkout, ".kogen", "project.yaml")
	source, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, &ConfigError{Path: path, Issues: []Issue{{Detail: err.Error()}}}
	}
	return ParseConfig(path, source)
}

// ParseMachineConfig parses the machine config. Its only accepted top-level
// key is build; project-only settings have no machine-level meaning.
func ParseMachineConfig(path string, source []byte) (*MachineConfig, error) {
	root, err := parseMap(path, source)
	if err != nil {
		return nil, err
	}
	issues := make([]Issue, 0)
	unknownKeys(root, []string{"build"}, "machine", &issues)
	var build yamlmini.Mapping
	if raw, exists := root["build"]; exists {
		var ok bool
		build, ok = raw.(yamlmini.Mapping)
		if !ok {
			issues = append(issues, Issue{Detail: "build must be a map"})
		} else {
			issues = append(issues, validateBuild(build)...)
		}
	}
	if len(issues) != 0 {
		return nil, &ConfigError{Path: path, Issues: issues}
	}
	return &MachineConfig{Build: build, Raw: root}, nil
}

// LoadMachineConfig loads ~/.kogen/config.yaml. A missing file means there are
// no machine overrides.
func LoadMachineConfig(home string) (*MachineConfig, error) {
	path := filepath.Join(home, ".kogen", "config.yaml")
	source, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &MachineConfig{Build: yamlmini.Mapping{}}, nil
	}
	if err != nil {
		return nil, &ConfigError{Path: path, Issues: []Issue{{Detail: err.Error()}}}
	}
	return ParseMachineConfig(path, source)
}

func parseMap(path string, source []byte) (yamlmini.Mapping, error) {
	value, err := yamlmini.Parse(source)
	if err != nil {
		issue, _ := err.(*yamlmini.Issue)
		line, detail := 0, err.Error()
		if issue != nil {
			line, detail = issue.Line, issue.Message
		}
		return nil, &ConfigError{Path: path, Issues: []Issue{{Line: line, Detail: detail}}}
	}
	root, ok := value.(yamlmini.Mapping)
	if !ok {
		return nil, &ConfigError{Path: path, Issues: []Issue{{Detail: "project config must be a YAML map"}}}
	}
	return root, nil
}

// GitPort is the supervised Git effect used by project resolution.
type GitPort interface {
	Exec(context.Context, []string, []byte, contract.GitPolicy) (contract.GitResult, error)
}

// PolicyForDirectory builds the policy for each Git operation. The resolver
// never invokes Git through a shell or inherits an implicit command environment.
type PolicyForDirectory func(directory string) contract.GitPolicy

// Options controls project resolution. Paths are resolved against CWD; CWD,
// HOME and the Git policy must be supplied by the caller for deterministic use.
type Options struct {
	CWD      string
	Project  string
	Origin   string
	Base     string
	Home     string
	Provider string
	Machine  *MachineConfig
	Git      GitPort
	Policy   PolicyForDirectory
}

// Resolution is the canonical project identity shared by project commands.
type Resolution struct {
	Checkout  string
	Origin    string
	Base      string
	StateRoot string
	Config    *Config
	Machine   *MachineConfig
	Roles     contract.RoleManifest
	Land      string
}

// Resolve applies project → origin → base precedence from §1.6. The Git calls
// all pass through the supplied supervised GitPort.
func Resolve(ctx context.Context, options Options) (*Resolution, error) {
	if ctx == nil {
		return nil, fmt.Errorf("project resolution context is required")
	}
	if options.CWD == "" {
		return nil, fmt.Errorf("project working directory is required")
	}
	cwd, err := filepath.Abs(options.CWD)
	if err != nil {
		return nil, fmt.Errorf("project working directory: %w", err)
	}
	projectPath := cwd
	if options.Project != "" {
		projectPath = absoluteFrom(cwd, options.Project)
	}
	checkoutPath, err := filepath.EvalSymlinks(projectPath)
	if err != nil {
		return nil, fmt.Errorf("project unavailable: %s", projectPath)
	}
	checkoutPath, err = filepath.Abs(checkoutPath)
	if err != nil {
		return nil, fmt.Errorf("project unavailable: %s", projectPath)
	}
	info, err := os.Stat(checkoutPath)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("project unavailable: %s", projectPath)
	}
	if options.Git == nil || options.Policy == nil {
		return nil, fmt.Errorf("project Git port and policy are required")
	}
	top, err := gitText(ctx, options, checkoutPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not a Git work tree: %s", checkoutPath)
	}
	checkoutPath = strings.TrimSpace(top)
	if !filepath.IsAbs(checkoutPath) {
		checkoutPath = filepath.Join(projectPath, checkoutPath)
	}
	checkoutPath, err = filepath.EvalSymlinks(checkoutPath)
	if err != nil {
		return nil, fmt.Errorf("not a Git work tree: %s", projectPath)
	}
	checkoutPath, err = filepath.Abs(checkoutPath)
	if err != nil {
		return nil, fmt.Errorf("not a Git work tree: %s", projectPath)
	}

	config, err := LoadConfig(checkoutPath)
	if err != nil {
		return nil, err
	}
	if options.Home == "" {
		return nil, fmt.Errorf("project home directory is required")
	}
	machine := options.Machine
	if machine == nil {
		machine, err = LoadMachineConfig(options.Home)
		if err != nil {
			return nil, err
		}
	}
	provider := options.Provider
	if provider == "" {
		provider = os.Getenv("KOGEN_BENCH_PROVIDER")
	}
	if provider != "chatgpt" && provider != "grok" {
		provider = "chatgpt"
	}
	roles, err := ResolveRoles(provider, config, machine)
	if err != nil {
		return nil, err
	}
	land := LandPolicy(config, machine)
	origin := checkoutPath
	if options.Origin != "" {
		origin = absoluteFrom(cwd, options.Origin)
	} else if remote, remoteErr := gitText(ctx, options, checkoutPath, "config", "--get", "remote.origin.url"); remoteErr == nil {
		if local := localOrigin(strings.TrimSpace(remote), checkoutPath, options.Home); local != "" {
			if canonical, canonicalErr := filepath.EvalSymlinks(local); canonicalErr == nil {
				origin = canonical
			}
		}
	}
	if canonical, canonicalErr := filepath.EvalSymlinks(origin); canonicalErr == nil {
		origin = canonical
	}

	base := options.Base
	if base == "" && config != nil {
		base = config.Base
	}
	if base != "" {
		if _, err := gitText(ctx, options, origin, "rev-parse", "--verify", base+"^{commit}"); err != nil {
			return nil, fmt.Errorf("base unavailable: %s", base)
		}
	} else if filepath.Clean(origin) == filepath.Clean(checkoutPath) {
		if head, headErr := gitText(ctx, options, checkoutPath, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); headErr == nil && strings.TrimSpace(head) != "" {
			base = strings.TrimSpace(head)
		}
	} else if head, headErr := gitText(ctx, options, origin, "symbolic-ref", "--short", "HEAD"); headErr == nil && strings.TrimSpace(head) != "" {
		base = strings.TrimSpace(head)
	}
	if base == "" {
		if branch, branchErr := gitText(ctx, options, checkoutPath, "branch", "--show-current"); branchErr == nil {
			base = strings.TrimSpace(branch)
		}
	}
	if base == "" {
		return nil, fmt.Errorf("base unavailable: no configured, origin, or current branch")
	}
	key := StateKey(checkoutPath)
	return &Resolution{
		Checkout:  checkoutPath,
		Origin:    origin,
		Base:      base,
		StateRoot: filepath.Join(options.Home, ".kogen", "workspaces", key),
		Config:    config,
		Machine:   machine,
		Roles:     roles,
		Land:      land,
	}, nil
}

// StateKey uses the canonical checkout path so symlinked spellings share one
// state root.
func StateKey(checkout string) string {
	base := filepath.Base(filepath.Clean(checkout))
	var safe strings.Builder
	lastDash := false
	for _, r := range base {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r) {
			safe.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			safe.WriteByte('-')
			lastDash = true
		}
	}
	name := safe.String()
	if len(name) > 40 {
		name = name[:40]
	}
	if name == "" {
		name = "checkout"
	}
	digest := sha256.Sum256([]byte(checkout))
	return name + "-" + hex.EncodeToString(digest[:5])
}

func gitText(ctx context.Context, options Options, directory string, args ...string) (string, error) {
	result, err := options.Git.Exec(ctx, args, nil, options.Policy(directory))
	if err != nil {
		return "", err
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 || result.Process.TimedOut || result.Process.Unavailable {
		return "", fmt.Errorf("git %s failed", strings.Join(args, " "))
	}
	return string(result.Stdout), nil
}

func localOrigin(remote, checkout, home string) string {
	if strings.HasPrefix(remote, "file://") {
		u, err := url.Parse(remote)
		if err != nil || (u.Host != "" && u.Host != "localhost") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return ""
		}
		return u.Path
	}
	parsed, parseErr := url.Parse(remote)
	if parseErr != nil || parsed.Scheme != "" || strings.Contains(remote, "@") && !strings.Contains(remote, "/") && strings.Contains(remote, ":") {
		return ""
	}
	if strings.HasPrefix(remote, "~/") {
		if home == "" {
			return ""
		}
		remote = filepath.Join(home, remote[2:])
	}
	path := absoluteFrom(checkout, remote)
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
		return path
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return path
	}
	return ""
}

func absoluteFrom(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(base, path))
}
