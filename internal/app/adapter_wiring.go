package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"kogen-go/internal/acceptance"
	acceptancecommand "kogen-go/internal/acceptance/command"
	"kogen-go/internal/acceptance/exunit"
	"kogen-go/internal/acceptance/rails"
	authgrok "kogen-go/internal/auth/grok"
	"kogen-go/internal/auth/vault"
	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/provider/grok"
	"kogen-go/internal/provider/retry"
	providersession "kogen-go/internal/provider/session"
	"kogen-go/internal/provider/sse"
	"kogen-go/internal/provider/transport"
	"kogen-go/internal/provider/wire"
	"kogen-go/internal/safefs"
	shapeaudit "kogen-go/internal/shape/audit"
	shaperun "kogen-go/internal/shape/run"
	shapesession "kogen-go/internal/shape/session"
	"kogen-go/internal/shape/validate"
	"kogen-go/internal/yamlmini"
)

// shapeAdapters owns the effectful adapters used by the public Shape route.
// The component implementations remain independent of CLI parsing and output.
type shapeAdapters struct {
	processes    contract.ProcessRunner
	git          contract.GitPort
	env          process.Environment
	resolved     *project.Resolution
	runDir       string
	runRoot      contract.RootedFS
	trees        acceptance.TreeSnapshotter
	checks       []contract.CheckSpec
	formatChecks []contract.CheckSpec
	setup        []contract.CheckSpec
	childEnv     process.Environment
	provider     *shapeProvider
	closers      []func() error
}

func (a *shapeAdapters) close() error {
	var result error
	for index := len(a.closers) - 1; index >= 0; index-- {
		result = errors.Join(result, a.closers[index]())
	}
	a.closers = nil
	return result
}

func newShapeAdapters(ctx context.Context, cli *CLI, ports runtimePorts, resolved *project.Resolution, runDir string, runRoot contract.RootedFS) (*shapeAdapters, error) {
	if ctx == nil || cli == nil || resolved == nil || runRoot == nil {
		return nil, errors.New("Shape adapter wiring requires a context, CLI, project and rooted run directory")
	}
	checks, err := projectCheckSpecs(resolved.Config, "acceptance_checks")
	if err != nil {
		return nil, err
	}
	setup, err := projectCheckSpecs(resolved.Config, "setup")
	if err != nil {
		return nil, err
	}
	formatChecks, err := projectCheckSpecs(resolved.Config, "checks")
	if err != nil {
		return nil, err
	}
	projectEnv, err := configuredProjectEnvironment(resolved.Config)
	if err != nil {
		return nil, err
	}
	childEnv, err := process.BuildChildEnvironment(ctx, ports.processes, process.EnvironmentRequest{
		Base: ports.env, RunDir: runDir, RunRoot: runRoot,
		ProjectRoot: resolved.Checkout, Workspace: resolved.Checkout,
		Project: projectEnv,
	})
	if err != nil {
		return nil, fmt.Errorf("build Shape child environment: %w", err)
	}
	baseCommit, err := gitio.NewRefPort(ports.workspaceGit, gitio.WorkspacePolicy(resolved.Origin, ports.env)).ResolveCommit(ctx, resolved.Base)
	if err != nil {
		return nil, fmt.Errorf("resolve Shape base: %w", err)
	}
	base, err := gitio.LoadBaseMetadata(ctx, ports.workspaceGit, gitio.WorkspacePolicy(resolved.Checkout, ports.env), baseCommit)
	if err != nil {
		return nil, fmt.Errorf("load Shape base metadata: %w", err)
	}
	trees := acceptance.CandidateTree{
		Git: ports.workspaceGit, Policy: gitio.WorkspacePolicy(resolved.Checkout, ports.env), Base: base,
	}
	transportClient, err := transport.NewFromEnvironment(nil)
	if err != nil {
		return nil, err
	}
	provider, err := newShapeProvider(cli.Env, resolved, transportClient)
	if err != nil {
		return nil, err
	}
	adapters := &shapeAdapters{
		processes: ports.processes, git: ports.workspaceGit, env: childEnv,
		resolved: resolved, runDir: runDir, runRoot: runRoot,
		trees: trees, checks: checks, formatChecks: formatChecks, setup: setup, childEnv: childEnv, provider: provider,
	}
	if provider.close != nil {
		adapters.closers = append(adapters.closers, provider.close)
	}
	return adapters, nil
}

// shapeAcceptance is the selected adapter boundary for Shape's staged test,
// base execution, formatting and path derivation.
type shapeAcceptance struct {
	kind    string
	config  acceptancecommand.Config
	useMise bool
	process contract.ProcessRunner
	trees   acceptance.TreeSnapshotter
	roots   contract.RootOpener
	runDir  string
	env     process.Environment
}

func (a shapeAcceptance) Paths(slug string) (validate.Paths, error) {
	var source, candidate string
	var err error
	switch a.kind {
	case "exunit":
		source, err = exunit.SourcePath(slug)
		if err == nil {
			candidate, err = exunit.CandidatePath(slug)
		}
	case "rails":
		source, err = rails.SourcePath(slug)
		if err == nil {
			candidate, err = rails.CandidatePath(slug)
		}
	default:
		source, err = a.config.SourcePath(slug)
		if err == nil {
			candidate, err = a.config.CandidatePath(slug)
		}
	}
	return validate.Paths{Source: source, Candidate: candidate}, err
}

func (a shapeAcceptance) BaseResults(ctx context.Context, checkout, source string, itemIDs []string) (map[string]bool, error) {
	slug := a.slugFromSource(source)
	paths, err := a.Paths(slug)
	if err != nil || paths.Source != source {
		return nil, errors.New("Shape base adapter received an unexpected source path")
	}
	root, err := a.roots.OpenRoot(checkout)
	if err != nil {
		return nil, fmt.Errorf("open Shape base checkout: %w", err)
	}
	defer closeRootFS(root)
	contents, err := root.ReadFile(source)
	if err != nil {
		return nil, fmt.Errorf("read Shape acceptance source: %w", err)
	}
	info, err := root.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Shape acceptance source is not a regular file")
	}
	if err := root.MkdirAll(path.Dir(paths.Candidate), 0o755); err != nil {
		return nil, fmt.Errorf("prepare Shape base candidate directory: %w", err)
	}
	if _, err := root.Lstat(paths.Candidate); err == nil {
		return nil, errors.New("Shape base candidate path is occupied")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := root.Publish(paths.Candidate, contents, info.Mode().Perm(), safefs.PublicationCreateOnly); err != nil {
		return nil, fmt.Errorf("stage Shape base acceptance source: %w", err)
	}
	defer func() { _ = root.Remove(paths.Candidate) }()
	result, err := a.run(ctx, slug, itemIDs, checkout)
	if err != nil {
		return nil, err
	}
	return result.ItemPass, nil
}

func (a shapeAcceptance) slugFromSource(source string) string {
	extension := a.config.Extension
	if a.kind == "exunit" {
		extension = exunit.Extension
	}
	if extension != "" {
		return strings.TrimSuffix(path.Base(source), extension)
	}
	return slugFromAcceptancePath(source)
}

func (a shapeAcceptance) run(ctx context.Context, slug string, itemIDs []string, checkout string) (acceptance.Result, error) {
	switch a.kind {
	case "exunit":
		return (&exunit.Runner{Processes: a.process, Trees: a.trees, Roots: a.roots}).Run(ctx, exunit.Request{
			Slug: slug, Workdir: checkout, RunDir: a.runDir, Environment: a.env,
			ExpectedItems: itemIDs, UseMise: a.useMise,
		})
	default:
		return (&acceptancecommand.Runner{Processes: a.process, Trees: a.trees, Roots: a.roots}).Run(ctx, acceptancecommand.Request{
			Config: a.config, Slug: slug, Workdir: checkout, RunDir: a.runDir,
			Environment: a.env, ExpectedItems: itemIDs,
		})
	}
}

func slugFromAcceptancePath(source string) string {
	base := path.Base(source)
	for _, suffix := range []string{"_test.exs", "_test.rb", ".t.sh"} {
		base = strings.TrimSuffix(base, suffix)
	}
	if strings.HasPrefix(source, ".kogen/acceptance/") {
		name := strings.TrimPrefix(source, ".kogen/acceptance/")
		for _, suffix := range []string{"_test.exs", "_test.rb", ".t.sh"} {
			name = strings.TrimSuffix(name, suffix)
		}
		return name
	}
	return base
}

func selectShapeAcceptance(resolved *project.Resolution, runDir string, processes contract.ProcessRunner, trees acceptance.TreeSnapshotter, environment process.Environment) (shapeAcceptance, error) {
	settings := yamlmini.Mapping{}
	if resolved.Config != nil {
		settings, _ = resolved.Config.Raw["acceptance"].(yamlmini.Mapping)
	}
	selected, _ := settings["adapter"].(string)
	if selected == "" {
		switch {
		case rails.Detected(resolved.Checkout):
			selected = "rails"
		case regularProjectFile(resolved.Checkout, "mix.exs"):
			selected = "exunit"
		default:
			selected = "command"
		}
	}
	config := acceptancecommand.Config{
		Extension: ".t.sh", CandidateDir: "test/acceptance", Run: []string{"sh", "{path}"},
		Timeout: acceptance.NormalizeTimeout(0),
	}
	switch selected {
	case "rails":
		config.Extension = rails.AcceptanceExtension
		config.CandidateDir = rails.CandidateDirectory
		config.Run = rails.RunnerCommand()
		if timeout, ok := settings["timeout_ms"].(int); ok {
			config.Timeout = time.Duration(timeout) * time.Millisecond
		}
	case "exunit":
		// ExUnit has a dedicated formatter-backed runner below; the shared
		// command configuration is retained only for format metadata.
	case "command":
		if raw, ok := settings["ext"].(string); ok && raw != "" {
			config.Extension = raw
		} else if _, explicitlyConfigured := settings["adapter"]; explicitlyConfigured {
			return shapeAcceptance{}, errors.New("acceptance command requires ext")
		}
		if raw, ok := settings["candidate_dir"].(string); ok && raw != "" {
			config.CandidateDir = raw
		}
		if raw, ok := settings["run"].(yamlmini.Sequence); ok && len(raw) != 0 {
			config.Run = make([]string, len(raw))
			for index, item := range raw {
				value, ok := item.(string)
				if !ok {
					return shapeAcceptance{}, fmt.Errorf("acceptance.run item %d is invalid", index+1)
				}
				config.Run[index] = value
			}
		}
		if timeout, ok := settings["timeout_ms"].(int); ok {
			config.Timeout = time.Duration(timeout) * time.Millisecond
		}
	default:
		return shapeAcceptance{}, fmt.Errorf("unknown acceptance adapter %q", selected)
	}
	if err := config.Validate(); err != nil {
		return shapeAcceptance{}, err
	}
	roots := safefs.Opener{}
	adapterEnv := make(process.Environment, len(environment))
	for key, value := range environment {
		adapterEnv[key] = value
	}
	if selected == "rails" {
		for key, value := range rails.ChildEnvironment(filepath.Join(resolved.Checkout, "vendor", "cache")) {
			adapterEnv[key] = value
		}
	}
	return shapeAcceptance{
		kind: selected, config: config, useMise: selected == "exunit" && (regularProjectFile(resolved.Checkout, ".tool-versions") || regularProjectFile(resolved.Checkout, "mise.toml")),
		process: processes, trees: trees, roots: roots, runDir: runDir, env: adapterEnv,
	}, nil
}

func regularProjectFile(root, name string) bool {
	info, err := os.Stat(filepath.Join(root, name))
	return err == nil && info.Mode().IsRegular()
}

func projectCheckSpecs(config *project.Config, field string) ([]contract.CheckSpec, error) {
	if config == nil {
		return nil, nil
	}
	rows, ok := config.Raw[field].(yamlmini.Sequence)
	if !ok {
		return nil, nil
	}
	checks := make([]contract.CheckSpec, 0, len(rows))
	for index, row := range rows {
		mapping, ok := row.(yamlmini.Mapping)
		if !ok {
			return nil, fmt.Errorf("project %s row %d is invalid", field, index+1)
		}
		name, _ := mapping["name"].(string)
		argv, ok := mapping["argv"].(yamlmini.Sequence)
		if !ok || len(argv) == 0 {
			return nil, fmt.Errorf("project %s row %d has no argv", field, index+1)
		}
		args := make([]string, len(argv))
		for argIndex, item := range argv {
			value, ok := item.(string)
			if !ok || value == "" {
				return nil, fmt.Errorf("project %s row %d has invalid argv", field, index+1)
			}
			args[argIndex] = value
		}
		timeoutMS, _ := mapping["timeout_ms"].(int)
		if name == "" || timeoutMS <= 0 {
			return nil, fmt.Errorf("project %s row %d has invalid name or timeout", field, index+1)
		}
		checks = append(checks, contract.CheckSpec{Name: name, Program: args[0], Args: args[1:], Timeout: time.Duration(timeoutMS) * time.Millisecond})
	}
	return checks, nil
}

func (a *shapeAdapters) acceptance(slug, source string) (shapeAcceptance, error) {
	selected, err := selectShapeAcceptance(a.resolved, a.runDir, a.processes, a.trees, a.childEnv)
	if err != nil {
		return shapeAcceptance{}, err
	}
	paths, err := selected.Paths(slug)
	if err != nil || paths.Source != source {
		return shapeAcceptance{}, fmt.Errorf("selected acceptance adapter paths do not match %s", source)
	}
	return selected, nil
}

func (a *shapeAdapters) validator(ctx context.Context, slug string, request []byte, gatePaths []string) (validate.Options, error) {
	adapter, err := selectShapeAcceptance(a.resolved, a.runDir, a.processes, a.trees, a.childEnv)
	if err != nil {
		return validate.Options{}, err
	}
	paths, err := adapter.Paths(slug)
	if err != nil {
		return validate.Options{}, err
	}
	adapter, err = a.acceptance(slug, paths.Source)
	if err != nil {
		return validate.Options{}, err
	}
	checkSpecs := append([]contract.CheckSpec(nil), a.checks...)
	if adapter.kind == "rails" && len(checkSpecs) == 0 {
		checkSpecs = []contract.CheckSpec{{Name: "ruby-syntax", Program: "ruby", Args: []string{"-c", "{path}"}, Timeout: 600 * time.Second}}
	}
	checks := shapeCheckRunner{process: a.processes, trees: a.trees, runDir: a.runDir, env: a.childEnv, specs: checkSpecs}
	setup := shapeSetupRunner{process: a.processes, trees: a.trees, runDir: a.runDir, env: a.childEnv, specs: a.setup}
	formatter, err := shapeFormatterFor(a.resolved, adapter, a.formatChecks, a.childEnv, a.processes, a.runDir)
	if err != nil {
		return validate.Options{}, err
	}
	return validate.Options{
		Checkout: a.resolved.Checkout, Slug: slug, Request: request, GatePaths: gatePaths,
		Adapter: adapter, Setup: setup, Formatter: formatter, Checks: checks,
		Trees: a.trees, Roots: safefs.Opener{},
	}, nil
}

type shapeCheckRunner struct {
	process contract.ProcessRunner
	trees   acceptance.TreeSnapshotter
	runDir  string
	env     process.Environment
	specs   []contract.CheckSpec
}

func (r shapeCheckRunner) Run(ctx context.Context, workspace, candidate string) ([]contract.CheckResult, error) {
	adapter := acceptance.Adapter{Processes: r.process, Trees: r.trees, Roots: safefs.Opener{}, RunDir: r.runDir}
	results := make([]contract.CheckResult, 0, len(r.specs))
	for _, spec := range r.specs {
		spec.Env, _ = acceptance.SortedEnvironment(r.env)
		for index := range spec.Args {
			spec.Args[index] = strings.ReplaceAll(spec.Args[index], "{path}", filepath.Join(workspace, filepath.FromSlash(candidate)))
		}
		result, err := adapter.Run(ctx, workspace, spec)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

type shapeSetupRunner struct {
	process contract.ProcessRunner
	trees   acceptance.TreeSnapshotter
	runDir  string
	env     process.Environment
	specs   []contract.CheckSpec
}

func (r shapeSetupRunner) Run(ctx context.Context, workspace string) error {
	if len(r.specs) == 0 {
		return nil
	}
	adapter := acceptance.Adapter{Processes: r.process, Trees: r.trees, Roots: safefs.Opener{}, RunDir: r.runDir}
	for _, spec := range r.specs {
		spec.Env, _ = acceptance.SortedEnvironment(r.env)
		result, err := adapter.Run(ctx, workspace, spec)
		if err != nil || result.Status != contract.CheckGreen {
			return fmt.Errorf("setup %s failed", spec.Name)
		}
	}
	return nil
}

type shapeFormatter struct {
	process    contract.ProcessRunner
	runDir     string
	env        process.Environment
	argv       []string
	sourceOnly bool
}

func (f shapeFormatter) Format(ctx context.Context, workspace string, paths []string) (bool, error) {
	if len(f.argv) == 0 {
		return false, nil
	}
	formatPaths := paths
	if f.sourceOnly && len(paths) > 1 {
		formatPaths = paths[1:]
	}
	argv := make([]string, len(f.argv))
	hasPath := false
	for index, value := range f.argv {
		if strings.Contains(value, "{path}") {
			hasPath = true
			value = strings.ReplaceAll(value, "{path}", filepath.Join(workspace, filepath.FromSlash(formatPaths[0])))
		}
		argv[index] = value
	}
	if hasPath {
		for _, file := range formatPaths[1:] {
			argv = append(argv, filepath.Join(workspace, filepath.FromSlash(file)))
		}
	} else {
		for _, file := range formatPaths {
			argv = append(argv, filepath.Join(workspace, filepath.FromSlash(file)))
		}
	}
	environment, err := acceptance.SortedEnvironment(f.env)
	if err != nil {
		return false, err
	}
	result, err := f.process.Run(ctx, contract.ProcessSpec{
		Executable: argv[0], Args: argv[1:], Dir: workspace, Env: environment,
		Timeout: 10 * time.Minute, OutputLimit: process.MaximumOutputLimit,
		OutputTailLimit: process.OutputTailBytes, LogPath: filepath.Join(f.runDir, "logs", "shape-format.log"),
	})
	if err != nil {
		return false, err
	}
	if result.Unavailable || result.ExitStatus == nil || *result.ExitStatus == 126 || *result.ExitStatus == 127 {
		return true, nil
	}
	if result.TimedOut || *result.ExitStatus != 0 {
		return false, errors.New("formatter command failed")
	}
	return false, nil
}

func shapeFormatterFor(resolved *project.Resolution, adapter shapeAcceptance, checks []contract.CheckSpec, env process.Environment, runner contract.ProcessRunner, runDir string) (validate.Formatter, error) {
	var argv []string
	sourceOnly := false
	configured := false
	if resolved.Config != nil {
		if values, ok := resolved.Config.Raw["format"].(yamlmini.Sequence); ok {
			configured = true
			argv = make([]string, 0, len(values))
			for _, value := range values {
				text, ok := value.(string)
				if !ok {
					return nil, errors.New("project format argv contains a non-string")
				}
				argv = append(argv, text)
			}
		}
	}
	if len(argv) == 0 {
		switch adapter.kind {
		case "exunit":
			argv = exunit.Formatter(checks, "file.exs")
			if len(argv) > 0 && argv[len(argv)-1] == "file.exs" {
				argv = argv[:len(argv)-1]
			}
			sourceOnly = true
		case "rails":
			contents, err := os.ReadFile(filepath.Join(resolved.Checkout, "Gemfile"))
			if err != nil {
				return nil, err
			}
			argv = rails.Formatter(string(contents))
			sourceOnly = true
		}
	}
	if len(argv) == 0 {
		return nil, nil
	}
	return shapeFormatter{process: runner, runDir: runDir, env: env, argv: argv, sourceOnly: sourceOnly && !configured}, nil
}

type shapeProvider struct {
	env       process.Environment
	resolved  *project.Resolution
	transport *transport.Client
	vault     *vault.Store
	grokAuth  *authgrok.Client
	grok      *grok.Adapter
	mode      wire.Mode
	close     func() error
	last      contract.ConversationIdentity
}

func newShapeProvider(env process.Environment, resolved *project.Resolution, transportClient *transport.Client) (*shapeProvider, error) {
	provider := &shapeProvider{env: env, resolved: resolved, transport: transportClient, mode: wire.ModeOwned}
	home := env["HOME"]
	if home == "" {
		return nil, errors.New("Shape requires HOME")
	}
	store, err := vault.Open(home)
	if err != nil {
		return nil, err
	}
	provider.vault = store
	provider.close = store.Close
	settings, ok := resolved.Roles.Effective[contract.RoleName("shaper")]
	if !ok {
		_ = store.Close()
		return nil, errors.New("Shape effective shaper role is missing")
	}
	if settings.Provider == "grok" {
		issuer := env["KOGEN_AUTH_URL"]
		auth, err := authgrok.New(authgrok.Options{
			Home: home, Label: "default", IssuerURL: issuer,
			AllowLocalHTTP: issuer != "", Store: store,
		})
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		provider.grokAuth = auth
		adapter, err := grok.New(grok.Config{Auth: auth, Transport: transportClient, Endpoint: env["KOGEN_PROVIDER_URL"]})
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		provider.grok = adapter
	} else if env["KOGEN_AUTH_PATH"] != "" {
		provider.mode = wire.ModeInjected
	}
	return provider, nil
}

var _ shaperun.Turner = (*shapeProvider)(nil)
var _ shapeaudit.Transport = (*shapeProvider)(nil)

func (p *shapeProvider) Turn(ctx context.Context, request shaperun.TurnRequest, accounting shaperun.AttemptAccounting) (shaperun.TurnResponse, error) {
	identity := request.Conversation.Identity()
	p.last = identity
	return p.invoke(ctx, request.Conversation, identity.Role, request.Settings, request.Instructions, request.AllowedTools, request.ToolChoice, accounting)
}

func (p *shapeProvider) Audit(ctx context.Context, prompt shapeaudit.Prompt, accounting shapeaudit.AttemptAccounting) (string, error) {
	if p.last.RunID == "" {
		return "", errors.New("Shape auditor request has no run affinity")
	}
	epoch := "requirement"
	if strings.Contains(prompt.System, "acceptance test auditor") {
		epoch = "test_audit"
	}
	identity, err := providersession.Bind(providersession.Binding{
		RunID: p.last.RunID, CacheKey: p.last.CacheKey, Role: prompt.AssignedRole,
		Provider: prompt.Role.Provider, Model: prompt.Role.Model, Effort: prompt.Role.Effort,
		Stage: "shape", Attempt: "audit", Rung: "audit", Epoch: epoch,
	})
	if err != nil {
		return "", err
	}
	identity.SessionID = p.last.CacheKey
	conversation, err := providersession.New(identity)
	if err != nil {
		return "", err
	}
	if err := conversation.AppendUser(prompt.User); err != nil {
		return "", err
	}
	result, err := p.invoke(ctx, conversation, prompt.AssignedRole, prompt.Role, prompt.System, nil, "none", auditAttemptAccounting{accounting})
	return result.Text, err
}

type auditAttemptAccounting struct{ shapeaudit.AttemptAccounting }

func (p *shapeProvider) invoke(ctx context.Context, conversation *providersession.Conversation, role contract.RoleName, settings contract.RoleSettings, instructions string, allowed []string, choice string, accounting interface {
	Dispatch(shapesession.AttemptKind) error
	Complete(*contract.TokenUsage) error
}) (shaperun.TurnResponse, error) {
	var result shaperun.TurnResponse
	if p.grok != nil {
		before := cloneSessionHistory(conversation.ProtocolSession().History)
		response, err := p.grok.Respond(ctx, grok.Request{
			Conversation: conversation, Prefix: wire.DefaultPrefix(), RoleInstructions: instructions, Mode: retry.ModeShape,
		})
		for index, evidence := range response.Attempts {
			kind := shapesession.AttemptFirst
			if index > 0 {
				kind = shapesession.AttemptRetry
			}
			if dispatchErr := accounting.Dispatch(kind); dispatchErr != nil {
				return result, dispatchErr
			}
			if evidenceErr := recordOptionalRequest(accounting, evidence); evidenceErr != nil {
				return result, evidenceErr
			}
			if completeErr := accounting.Complete(evidence.Usage); completeErr != nil {
				return result, completeErr
			}
		}
		conversation.ProtocolSession().History = before
		if err != nil {
			return result, err
		}
		if response.Response == nil {
			return result, errors.New("Grok returned no response")
		}
		result.Text = response.Response.Text
		result.RawItems = cloneRawItems(response.Response.RawItems)
		result.ToolCalls = make([]sse.ToolCall, len(response.Response.ToolCalls))
		for index, call := range response.Response.ToolCalls {
			result.ToolCalls[index] = sse.ToolCall{ID: call.ID, Name: call.Name, Arguments: append(json.RawMessage(nil), call.Arguments...)}
		}
		result.Calls = []shaperun.ModelCall{{Role: role, Settings: settings}}
		return result, nil
	}
	if ctx == nil || conversation == nil || p == nil || p.transport == nil {
		return result, errors.New("ChatGPT Shape transport is unavailable")
	}
	identity := conversation.Identity()
	if identity.Provider != "chatgpt" || identity.Model != settings.Model || identity.Effort != settings.Effort || identity.Role != role {
		return result, shaperun.ErrRoleManifestMismatch
	}
	credential, err := p.chatGPTCredentials()
	if err != nil {
		return result, err
	}
	config := wire.Config{Mode: p.mode, EndpointOverride: p.env["KOGEN_PROVIDER_URL"], UserAgentVersion: "1.0.0"}
	encoded, err := wire.Encode(wire.Request{
		Config:       config,
		Credentials:  wire.Credentials{Kind: credential.Kind, AccessToken: credential.AccessToken, AccountID: credential.AccountID},
		Conversation: conversation, Prefix: wire.DefaultPrefix(), Model: settings.Model, Effort: settings.Effort,
		RoleInstructions: instructions, CallableTools: allowed, ToolChoice: choice,
	})
	if err != nil {
		return result, err
	}
	if err := accounting.Dispatch(shapesession.AttemptFirst); err != nil {
		return result, err
	}
	started := time.Now()
	evidence := contract.RequestEvidence{
		CacheKey: identity.CacheKey, ThreadID: identity.ThreadID, Role: role,
		Provider: settings.Provider, Model: settings.Model, Effort: settings.Effort,
		PrefixSHA256: []string{encoded.StaticPrefixSHA256}, BodyBytes: int64(len(encoded.Request.Body)),
		RequestedAt: started,
	}
	if endpoint, parseErr := url.Parse(encoded.Request.Endpoint); parseErr == nil {
		evidence.EndpointHost, evidence.EndpointPath = endpoint.Hostname(), endpoint.EscapedPath()
	}
	for name := range encoded.Request.Headers {
		evidence.RoutingHeaderNames = append(evidence.RoutingHeaderNames, http.CanonicalHeaderKey(name))
	}
	providerRequest := transport.Request{
		Provider: transport.ProviderChatGPT, Evidence: evidence,
		Prepare: func(prepareCtx context.Context) (*http.Request, error) {
			req, requestErr := http.NewRequestWithContext(prepareCtx, http.MethodPost, encoded.Request.Endpoint, bytes.NewReader(encoded.Request.Body))
			if requestErr != nil {
				return nil, requestErr
			}
			req.Header = encoded.Request.Headers.Clone()
			return req, nil
		},
	}
	response, err := p.transport.Do(ctx, providerRequest)
	if err != nil {
		var failure *transport.Failure
		if errors.As(err, &failure) {
			evidence = failure.Evidence
		}
		evidence.RespondedAt = time.Now()
		_ = recordOptionalRequest(accounting, evidence)
		_ = accounting.Complete(nil)
		return result, err
	}
	assembler := sse.NewAssembler()
	feedErr := assembler.Feed(response.Body)
	assembled, parseErr := assembler.Finish()
	if feedErr != nil && parseErr == nil {
		parseErr = feedErr
	}
	if response.TurnState != nil {
		conversation.ProtocolSession().Routing.CodexTurnState = append([]byte(nil), response.TurnState...)
		conversation.ProtocolSession().Routing.HasCodexTurnState = true
	}
	var usage *contract.TokenUsage
	if assembled != nil {
		usage = usageFromSSE(assembled.Usage)
	}
	evidence = response.Evidence
	evidence.Usage = usage
	if err := recordOptionalRequest(accounting, evidence); err != nil {
		_ = accounting.Complete(usage)
		return result, err
	}
	if err := accounting.Complete(usage); err != nil {
		return result, err
	}
	if parseErr != nil {
		return result, parseErr
	}
	result.Text = assembled.Text
	result.RawItems = cloneRawItems(assembled.RawItems)
	result.ToolCalls = append([]sse.ToolCall(nil), assembled.ToolCalls...)
	result.Calls = []shaperun.ModelCall{{Role: role, Settings: settings, Usage: usage, WallMS: uint64(time.Since(started).Milliseconds())}}
	return result, nil
}

func (p *shapeProvider) chatGPTCredentials() (wire.Credentials, error) {
	if p.mode == wire.ModeInjected {
		injected, err := vault.NewInjectedReader(p.env["KOGEN_AUTH_PATH"]).Read()
		if err != nil {
			return wire.Credentials{}, err
		}
		return wire.Credentials{Kind: wire.CredentialInjected, AccessToken: injected.AccessToken, AccountID: injected.AccountID}, nil
	}
	credential, exists, err := p.vault.GetChatGPT("default")
	if err != nil {
		return wire.Credentials{}, err
	}
	if !exists {
		return wire.Credentials{}, vault.ErrInjectedUnavailable
	}
	return wire.Credentials{Kind: wire.CredentialOwned, AccessToken: credential.AccessToken}, nil
}

func usageFromSSE(usage sse.Usage) *contract.TokenUsage {
	if usage.Input == nil && usage.CachedInput == nil && usage.CacheWrite == nil && usage.Output == nil && usage.Reasoning == nil {
		return nil
	}
	return &contract.TokenUsage{
		Input: int64FromUint(usage.Input), CachedInput: int64FromUint(usage.CachedInput),
		CacheWrite: int64FromUint(usage.CacheWrite), Output: int64FromUint(usage.Output),
		Reasoning: int64FromUint(usage.Reasoning),
	}
}

func int64FromUint(value *uint64) *int64 {
	if value == nil || *value > uint64(^uint64(0)>>1) {
		return nil
	}
	converted := int64(*value)
	return &converted
}

type evidenceRecorder interface {
	RecordRequest(contract.RequestEvidence) error
}

func recordOptionalRequest(accounting any, evidence contract.RequestEvidence) error {
	if recorder, ok := accounting.(evidenceRecorder); ok {
		return recorder.RecordRequest(evidence)
	}
	return nil
}

func cloneRawItems(items []json.RawMessage) []json.RawMessage {
	result := make([]json.RawMessage, len(items))
	for index := range items {
		result[index] = append(json.RawMessage(nil), items[index]...)
	}
	return result
}

func cloneSessionHistory(history []contract.SessionItem) []contract.SessionItem {
	result := make([]contract.SessionItem, len(history))
	for index, item := range history {
		result[index] = contract.SessionItem{Kind: item.Kind, Raw: append(json.RawMessage(nil), item.Raw...)}
	}
	return result
}
