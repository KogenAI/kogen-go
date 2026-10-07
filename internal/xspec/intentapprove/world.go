package intentapprove

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"kogen-go/internal/acceptance"
	"kogen-go/internal/approval/prepare"
	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/safefs"
	"kogen-go/internal/setupcache"
	"kogen-go/internal/yamlmini"
)

const (
	intentPathFormat     = ".kogen/intents/%s/intent.md"
	acceptancePathFormat = ".kogen/acceptance/%s.t.sh"
	approvalRefFormat    = "refs/kogen/intents/%s"
	fixtureTimestamp     = "2000-01-01T00:00:00Z"
)

var validIntent = []byte("---\ntitle: Replay greeting\nsize: small\ndomains: [app]\n---\nUpdate the greeting output.\n\n## Acceptance\n- A1: show the updated greeting\n\n## Verify\n- A1: test\n")

var validAcceptance = []byte("#!/bin/sh\nexit 0\n")

type world struct {
	mu          sync.Mutex
	root        string
	home        string
	origin      string
	checkout    string
	stateRoot   string
	runDir      string
	cacheDir    string
	control     string
	env         process.Environment
	git         *gitio.Runner
	project     *project.Resolution
	setup       *setupcache.Cache
	checks      *checkCounter
	baseCommits map[string]contract.ObjectID
	baseTrees   map[string]string
	baseSymbol  string
	closed      bool
}

func newWorld(ctx context.Context, seedSources bool) (*world, error) {
	if ctx == nil {
		return nil, errors.New("intentapprove: context is required")
	}
	root, err := os.MkdirTemp("", "kogen-xspec-intentapprove-")
	if err != nil {
		return nil, fmt.Errorf("intentapprove: create fixture root: %w", err)
	}
	w := &world{
		root: root, home: filepath.Join(root, "home"), origin: filepath.Join(root, "origin.git"),
		checkout: filepath.Join(root, "checkout"), stateRoot: filepath.Join(root, "state"),
		runDir: filepath.Join(root, "run"), cacheDir: filepath.Join(root, "cache"),
		control:     filepath.Join(root, "control"),
		baseCommits: map[string]contract.ObjectID{}, baseTrees: map[string]string{},
	}
	if err := w.initialize(ctx, seedSources); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return w, nil
}

func (w *world) initialize(ctx context.Context, seedSources bool) error {
	for _, directory := range []string{w.home, filepath.Join(w.home, ".config"), w.origin, w.checkout, w.stateRoot, w.runDir, w.cacheDir, w.control} {
		mode := os.FileMode(0o700)
		if directory == w.origin || directory == w.checkout {
			mode = 0o755
		}
		if err := os.MkdirAll(directory, mode); err != nil {
			return fmt.Errorf("intentapprove: create fixture directory: %w", err)
		}
	}
	w.env = process.Environment{
		"HOME": w.home, "XDG_CONFIG_HOME": filepath.Join(w.home, ".config"),
		"PATH": os.Getenv("PATH"), "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null",
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0", "GIT_PAGER": "cat", "PAGER": "cat",
		"GIT_AUTHOR_NAME": "Kogen XSpec", "GIT_AUTHOR_EMAIL": "xspec@kogen.invalid",
		"GIT_COMMITTER_NAME": "Kogen XSpec", "GIT_COMMITTER_EMAIL": "xspec@kogen.invalid",
	}
	w.git = gitio.NewWorkspace(process.Supervisor{})
	if err := w.initRepository(ctx, w.checkout, "checkout", false); err != nil {
		return fmt.Errorf("intentapprove: initialize checkout: %w", err)
	}
	if err := w.initRepository(ctx, w.origin, "origin", true); err != nil {
		return fmt.Errorf("intentapprove: initialize origin: %w", err)
	}
	for _, setting := range [][2]string{
		{"user.name", "Kogen XSpec"}, {"user.email", "xspec@kogen.invalid"},
		{"commit.gpgsign", "false"}, {"core.hooksPath", "/dev/null"},
	} {
		if err := w.gitOK(ctx, w.checkout, "config", "--local", setting[0], setting[1]); err != nil {
			return fmt.Errorf("intentapprove: configure fixture Git %s: %w", setting[0], err)
		}
	}
	if err := w.writeCheckout("README.md", []byte("private xspec fixture\n"), 0o644); err != nil {
		return fmt.Errorf("intentapprove: write fixture README: %w", err)
	}
	if seedSources {
		for _, slug := range []string{"alpha", "bravo"} {
			if err := w.writeSources(slug, validIntent, validAcceptance); err != nil {
				return fmt.Errorf("intentapprove: seed %s sources: %w", slug, err)
			}
		}
	}
	if err := w.gitOK(ctx, w.checkout, "add", "-A"); err != nil {
		return fmt.Errorf("intentapprove: stage fixture base: %w", err)
	}
	if err := w.gitOK(ctx, w.checkout, "commit", "--quiet", "-m", "xspec base"); err != nil {
		return fmt.Errorf("intentapprove: commit fixture base: %w", err)
	}
	if err := w.gitOK(ctx, w.checkout, "push", "--quiet", w.origin, "HEAD:refs/heads/main"); err != nil {
		return fmt.Errorf("intentapprove: push fixture base: %w", err)
	}
	cache, err := setupcache.Open(w.cacheDir)
	if err != nil {
		return fmt.Errorf("intentapprove: open fixture setup cache: %w", err)
	}
	w.setup = cache
	baseTrees := gitTreeSnapshot{git: w.git, env: w.env}
	delegate := &acceptance.Adapter{Processes: process.Supervisor{}, Trees: baseTrees, Roots: safefs.Opener{}, RunDir: w.runDir}
	w.checks = &checkCounter{delegate: delegate}
	w.project = &project.Resolution{
		Checkout: w.checkout, Origin: w.origin, Base: "refs/heads/main", StateRoot: w.stateRoot,
		Config: fixtureProjectConfig(w.control),
		Land:   "green",
	}
	return nil
}

// initRepository runs the two bootstrap commands through the process
// supervisor. gitio's normal runner inspects local config before every Git
// operation, which necessarily fails before an empty fixture is initialized.
func (w *world) initRepository(ctx context.Context, directory, name string, bare bool) error {
	args := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.excludesFile=/dev/null", "-c", "core.attributesFile=/dev/null", "init", "--quiet", "--initial-branch=main"}
	if bare {
		args = append(args, "--bare")
	}
	env := make([]string, 0, len(w.env))
	for key, value := range w.env {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	result, err := (process.Supervisor{}).Run(ctx, contract.ProcessSpec{
		Executable: "git", Args: args, Dir: directory, Env: env,
		Timeout: gitio.GitTimeout, OutputLimit: 1 << 20, OutputTailLimit: 4 << 10,
		LogPath: filepath.Join(w.runDir, "git-init-"+name+".log"),
	})
	if err != nil {
		return fmt.Errorf("intentapprove: supervise git init: %w", err)
	}
	if result.ExitStatus == nil || *result.ExitStatus != 0 || result.TimedOut || result.Unavailable {
		return fmt.Errorf("intentapprove: git init %s failed: %s", name, strings.TrimSpace(string(result.OutputTail)))
	}
	return nil
}

func fixtureProjectConfig(control string) *project.Config {
	setupMarker := filepath.Join(control, "setup")
	baselineMarker := filepath.Join(control, "baseline")
	acceptanceMarker := filepath.Join(control, "acceptance")
	return &project.Config{Raw: yamlmini.Mapping{
		"acceptance":        yamlmini.Mapping{"ext": ".t.sh"},
		"setup":             yamlmini.Sequence{checkConfig("setup", setupMarker, 500)},
		"checks":            yamlmini.Sequence{checkConfig("baseline", baselineMarker, 500)},
		"acceptance_checks": yamlmini.Sequence{checkConfig("acceptance", acceptanceMarker, 500)},
	}}
}

func checkConfig(name, marker string, timeoutMS int) yamlmini.Mapping {
	script := `state=$(cat ` + shellQuote(marker) + `); case "$state" in green) exit 0;; red) exit 1;; unavailable) exit 127;; timeout) sleep 2;; *) exit 1;; esac`
	return yamlmini.Mapping{
		"name":       name,
		"argv":       yamlmini.Sequence{"/bin/sh", "-c", script},
		"timeout_ms": strconv.Itoa(timeoutMS),
	}
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func (w *world) prepareDependencies() prepare.Dependencies {
	w.project.Config = fixtureProjectConfig(w.control)
	return prepare.Dependencies{
		Git:    w.git,
		Policy: func(directory string) contract.GitPolicy { return gitio.WorkspacePolicy(directory, w.env) },
		Roots:  safefs.Opener{}, Processes: process.Supervisor{}, Checks: w.checks,
		Trees: gitTreeSnapshot{git: w.git, env: w.env}, Scratch: scratchPort{git: w.git, env: w.env, parent: w.root},
		Setup: w.setup, Baselines: &baselineObserver{cache: w.setup},
		Manifest: prepare.ProtectionBuilder{},
	}
}

func (w *world) policy(directory string) contract.GitPolicy {
	return gitio.WorkspacePolicy(directory, w.env)
}

func (w *world) applyIdentity(ctx context.Context, identity string) error {
	if identity == "" {
		delete(w.env, "GIT_AUTHOR_NAME")
		delete(w.env, "GIT_AUTHOR_EMAIL")
		for _, key := range []string{"user.name", "user.email"} {
			result, err := w.git.Exec(ctx, []string{"config", "--local", "--unset-all", key}, nil, w.policy(w.checkout))
			if err != nil {
				return err
			}
			if result.Process.ExitStatus == nil || result.Process.TimedOut || result.Process.Unavailable {
				return fmt.Errorf("intentapprove: clear fixture Git identity %s", key)
			}
		}
		return nil
	}
	separator := strings.LastIndex(identity, " <")
	if separator <= 0 || !strings.HasSuffix(identity, ">") || strings.ContainsAny(identity, "\r\n\x00") {
		return errors.New("intentapprove: fixture identity must be Name <email>")
	}
	name := strings.TrimSpace(identity[:separator])
	email := identity[separator+2 : len(identity)-1]
	if name == "" || email == "" || strings.ContainsAny(email, "<> \t") {
		return errors.New("intentapprove: fixture identity must be Name <email>")
	}
	w.env["GIT_AUTHOR_NAME"], w.env["GIT_AUTHOR_EMAIL"] = name, email
	if err := w.gitOK(ctx, w.checkout, "config", "--local", "user.name", name); err != nil {
		return err
	}
	return w.gitOK(ctx, w.checkout, "config", "--local", "user.email", email)
}

func (w *world) gitOK(ctx context.Context, directory string, args ...string) error {
	result, err := w.git.Exec(ctx, args, nil, w.policy(directory))
	if err != nil {
		return fmt.Errorf("intentapprove: git %s: %w", args[0], err)
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 || result.Process.TimedOut || result.Process.Unavailable {
		return fmt.Errorf("intentapprove: git %s failed: %s", args[0], strings.TrimSpace(string(result.StderrTail)))
	}
	return nil
}

func (w *world) writeCheckout(name string, data []byte, mode os.FileMode) error {
	root, err := safefs.OpenRoot(w.checkout)
	if err != nil {
		return err
	}
	defer root.Close()
	if parent := path.Dir(name); parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	return root.Publish(name, data, mode, safefs.PublicationReplace)
}

func (w *world) writeSources(slug string, intentBytes, acceptanceBytes []byte) error {
	if !safeFixtureSlug(slug) {
		return errors.New("intentapprove: unsafe fixture slug")
	}
	root, err := safefs.OpenRoot(w.checkout)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(".kogen/intents/"+slug, 0o755); err != nil {
		return err
	}
	if err := root.MkdirAll(".kogen/acceptance", 0o755); err != nil {
		return err
	}
	if err := root.Publish(fmt.Sprintf(intentPathFormat, slug), intentBytes, 0o644, safefs.PublicationReplace); err != nil {
		return err
	}
	return root.Publish(fmt.Sprintf(acceptancePathFormat, slug), acceptanceBytes, 0o755, safefs.PublicationReplace)
}

func (w *world) readSources(slug string) ([]byte, []byte, error) {
	if !safeFixtureSlug(slug) {
		return nil, nil, errors.New("intentapprove: unsafe fixture slug")
	}
	root, err := safefs.OpenRoot(w.checkout)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	intentBytes, err := root.ReadFile(fmt.Sprintf(intentPathFormat, slug))
	if err != nil {
		return nil, nil, err
	}
	acceptanceBytes, err := root.ReadFile(fmt.Sprintf(acceptancePathFormat, slug))
	if err != nil {
		return nil, nil, err
	}
	return intentBytes, acceptanceBytes, nil
}

func (w *world) setControls(setupStatus, baselineStatus, acceptanceStatus string) error {
	root, err := safefs.OpenRoot(w.root)
	if err != nil {
		return err
	}
	defer root.Close()
	for name, status := range map[string]string{"control/setup": setupStatus, "control/baseline": baselineStatus, "control/acceptance": acceptanceStatus} {
		if err := root.Publish(name, []byte(status+"\n"), 0o600, safefs.PublicationReplace); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) clearMiseProbeLog() error {
	root, err := safefs.OpenRoot(w.runDir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove("logs/mise-env.log"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (w *world) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	var err error
	if w.setup != nil {
		err = errors.Join(err, w.setup.Close())
	}
	if w.root != "" {
		err = errors.Join(err, os.RemoveAll(w.root))
	}
	return err
}

func safeFixtureSlug(slug string) bool {
	if len(slug) < 3 || len(slug) > 48 {
		return false
	}
	for index, r := range slug {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' && index > 0 && index < len(slug)-1 {
			continue
		}
		return false
	}
	return true
}

type checkCounter struct {
	mu             sync.Mutex
	delegate       contract.AcceptanceAdapter
	setupCalls     int
	baselineCalls  int
	acceptanceRuns int
}

func (c *checkCounter) Run(ctx context.Context, directory string, spec contract.CheckSpec) (contract.CheckResult, error) {
	c.mu.Lock()
	switch spec.Name {
	case "setup":
		c.setupCalls++
	case "baseline":
		c.baselineCalls++
	case "acceptance":
		c.acceptanceRuns++
	}
	c.mu.Unlock()
	if err := clearAcceptanceLog(c.delegate, spec.Name); err != nil {
		return contract.CheckResult{}, err
	}
	return c.delegate.Run(ctx, directory, spec)
}

func clearAcceptanceLog(delegate contract.AcceptanceAdapter, name string) error {
	adapter, ok := delegate.(*acceptance.Adapter)
	if !ok || adapter.RunDir == "" {
		return nil
	}
	digest := sha256.Sum256([]byte(name))
	root, err := safefs.OpenRoot(adapter.RunDir)
	if err != nil {
		return err
	}
	defer root.Close()
	path := "logs/check-" + hex.EncodeToString(digest[:8]) + ".log"
	if err := root.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (c *checkCounter) baselineCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.baselineCalls
}

type gitTreeSnapshot struct {
	git contract.GitPort
	env process.Environment
}

func (s gitTreeSnapshot) Snapshot(ctx context.Context, directory string) (string, error) {
	policy := gitio.WorkspacePolicy(directory, s.env)
	result, err := s.git.Exec(ctx, []string{"rev-parse", "--verify", "HEAD^{tree}"}, nil, policy)
	if err != nil {
		return "", err
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 {
		return "", fmt.Errorf("intentapprove: snapshot tree failed: %s", strings.TrimSpace(string(result.StderrTail)))
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

type scratchPort struct {
	git    contract.GitPort
	env    process.Environment
	parent string
}

func (s scratchPort) OpenExactBase(ctx context.Context, request prepare.ScratchRequest) (prepare.ScratchWorkspace, error) {
	path, err := os.MkdirTemp(s.parent, "approval-base-")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil {
		return nil, err
	}
	policy := gitio.WorkspacePolicy(request.Origin, s.env)
	if err := checkedGit(ctx, s.git, policy, "clone", "--quiet", "--local", "--no-hardlinks", "--no-checkout", "--template=", "--", request.Origin, path); err != nil {
		_ = os.RemoveAll(path)
		return nil, err
	}
	policy.WorkingDirectory = path
	if err := checkedGit(ctx, s.git, policy, "checkout", "--quiet", "--detach", string(request.BaseCommit)); err != nil {
		_ = os.RemoveAll(path)
		return nil, err
	}
	return &scratchWorkspace{path: path, tree: request.BaseTree, commit: request.BaseCommit, git: s.git, env: s.env}, nil
}

func checkedGit(ctx context.Context, git contract.GitPort, policy contract.GitPolicy, args ...string) error {
	result, err := git.Exec(ctx, args, nil, policy)
	if err != nil {
		return err
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 || result.Process.TimedOut || result.Process.Unavailable {
		return fmt.Errorf("git %s failed: %s", args[0], strings.TrimSpace(string(result.StderrTail)))
	}
	return nil
}

type scratchWorkspace struct {
	path   string
	tree   contract.ObjectID
	commit contract.ObjectID
	git    contract.GitPort
	env    process.Environment
}

func (s *scratchWorkspace) Directory() string       { return s.path }
func (s *scratchWorkspace) Tree() contract.ObjectID { return s.tree }

func (s *scratchWorkspace) ResetToBase(ctx context.Context) error {
	return checkedGit(ctx, s.git, gitio.WorkspacePolicy(s.path, s.env), "reset", "--hard", string(s.commit))
}

func (s *scratchWorkspace) Close() error { return os.RemoveAll(s.path) }

type baselineObserver struct {
	cache  *setupcache.Cache
	called bool
	ran    bool
	reused bool
	key    prepare.BaselineKey
	rows   []prepare.BaselineRow
}

func (b *baselineObserver) GetOrCompute(ctx context.Context, key prepare.BaselineKey, compute func(context.Context) ([]prepare.BaselineRow, error)) (prepare.BaselineResult, error) {
	b.called = true
	b.key = key
	result, err := b.cache.GetOrCompute(ctx, key, func(ctx context.Context) ([]prepare.BaselineRow, error) {
		b.ran = true
		return compute(ctx)
	})
	b.reused = result.Reused
	b.rows = result.Rows
	return result, err
}

func (b *baselineObserver) GetCalled() bool { return b.called }

func (b *baselineObserver) ComputeRan() bool { return b.ran }

func (b *baselineObserver) Key() prepare.BaselineKey { return b.key }

func (b *baselineObserver) Reused() bool { return b.reused }

func (w *world) ensureBaseSymbol(ctx context.Context, symbol string) (string, error) {
	if symbol == "" {
		tree, err := gitio.NewRefPort(w.git, w.policy(w.origin)).ResolveTree(ctx, "refs/heads/main")
		return string(tree), err
	}
	if _, exists := w.baseCommits[symbol]; !exists {
		refs := gitio.NewRefPort(w.git, w.policy(w.origin))
		if w.baseSymbol == "" {
			commit, err := refs.ResolveCommit(ctx, "refs/heads/main")
			if err != nil {
				return "", err
			}
			tree, err := refs.ResolveTree(ctx, string(commit))
			if err != nil {
				return "", err
			}
			w.baseCommits[symbol], w.baseTrees[symbol], w.baseSymbol = commit, string(tree), symbol
			return string(tree), nil
		}
		if err := w.gitOK(ctx, w.checkout, "reset", "--quiet", "--hard", "refs/heads/main"); err != nil {
			return "", err
		}
		digest := sha256.Sum256([]byte(symbol))
		marker := ".kogen/xspec/base/" + hex.EncodeToString(digest[:8])
		if err := w.writeCheckout(marker, []byte(symbol+"\n"), 0o644); err != nil {
			return "", err
		}
		if err := w.gitOK(ctx, w.checkout, "add", "--", marker); err != nil {
			return "", err
		}
		if err := w.gitOK(ctx, w.checkout, "commit", "--quiet", "-m", "xspec base identity"); err != nil {
			return "", err
		}
		if err := w.gitOK(ctx, w.checkout, "push", "--quiet", w.origin, "HEAD:refs/heads/main"); err != nil {
			return "", err
		}
		commit, err := refs.ResolveCommit(ctx, "refs/heads/main")
		if err != nil {
			return "", err
		}
		tree, err := refs.ResolveTree(ctx, string(commit))
		if err != nil {
			return "", err
		}
		w.baseCommits[symbol], w.baseTrees[symbol], w.baseSymbol = commit, string(tree), symbol
		return string(tree), nil
	}
	if w.baseSymbol != symbol {
		commit := w.baseCommits[symbol]
		if err := w.gitOK(ctx, w.checkout, "reset", "--quiet", "--hard", string(commit)); err != nil {
			return "", err
		}
		if err := w.gitOK(ctx, w.origin, "update-ref", "refs/heads/main", string(commit)); err != nil {
			return "", err
		}
		w.baseSymbol = symbol
	}
	return w.baseTrees[symbol], nil
}

var _ prepare.ScratchPort = scratchPort{}
var _ prepare.ScratchWorkspace = (*scratchWorkspace)(nil)
var _ acceptance.TreeSnapshotter = gitTreeSnapshot{}
