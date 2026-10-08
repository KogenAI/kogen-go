package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kogen-go/internal/acceptance"
	"kogen-go/internal/cli/parse"
	"kogen-go/internal/contract"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
	"kogen-go/internal/provider/session"
	"kogen-go/internal/provider/sse"
	"kogen-go/internal/provider/tools"
	"kogen-go/internal/safefs"
	shaperun "kogen-go/internal/shape/run"
	shapesession "kogen-go/internal/shape/session"
	"kogen-go/internal/yamlmini"
)

type persistedShapeIdentity struct {
	RunID    string `json:"run_id"`
	CacheKey string `json:"cache_key"`
}

// shape runs the production Shape controller against the resolved project.
// It only writes controller artifacts in its private run directory and the
// two model-approved checkout paths enforced by the tool and validation ports.
func (cli *CLI) shape(command parse.Command, cwd string) int {
	startedAt := time.Now()
	ctx := context.Background()
	ports := cli.ports()
	resolved, exit, message := cli.resolveProject(ctx, command, cwd, ports)
	if exit != 0 {
		return cli.writeRawError(message, exit)
	}
	if command.Slug == nil || command.Request == nil {
		return cli.writeError("controller", "internal_error", "Shape received incomplete command inputs", 70)
	}
	runID, err := session.NewRunID()
	if err != nil {
		return cli.writeError("controller", "internal_error", "could not allocate a private Shape run", 70)
	}
	cacheKey, err := session.NewRunAffinityKey()
	if err != nil {
		return cli.writeError("controller", "internal_error", "could not allocate Shape cache affinity", 70)
	}
	runDir, _, runRoot, err := prepareRunDirectories(resolved, runID)
	if err != nil {
		return cli.writeError("environment", "shape_scratch_unavailable", "could not create private Shape state", 3)
	}
	defer closeRootFS(runRoot)
	if err := runRoot.MkdirAll("shape", 0o700); err != nil {
		return cli.writeError("environment", "shape_scratch_unavailable", "could not prepare private Shape scratch", 3)
	}
	scratchDir := filepath.Join(runDir, "shape")
	scratchRoot, err := safefs.OpenRoot(scratchDir)
	if err != nil {
		return cli.writeError("environment", "shape_scratch_unavailable", "could not open private Shape scratch", 3)
	}
	defer closeRoot(scratchRoot)
	identityDocument, err := json.Marshal(persistedShapeIdentity{RunID: runID, CacheKey: cacheKey})
	if err != nil || scratchRoot.Publish("session.json", identityDocument, 0o600, safefs.PublicationCreateOnly) != nil {
		return cli.writeError("environment", "shape_scratch_unavailable", "could not persist Shape run identity", 3)
	}
	request, requestIssue := readShapeRequest(cli.In, cwd, *command.Request)
	adapters, err := newShapeAdapters(ctx, cli, ports, resolved, runDir, runRoot)
	if err != nil {
		return cli.writeError("controller", "shape_run_unavailable", "could not initialize Shape adapters", 70)
	}
	defer func() { _ = adapters.close() }()
	acceptanceAdapter, err := selectShapeAcceptance(resolved, runDir, ports.processes, adapters.trees, adapters.childEnv)
	if err != nil {
		return cli.writeError("environment", "project_config_invalid", "could not select acceptance adapter", 3)
	}
	paths, err := acceptanceAdapter.Paths(*command.Slug)
	if err != nil {
		return cli.writeError("intent", "invalid_slug", "Slug must use lowercase letters, digits, and dashes.", 2)
	}
	validatorOptions, err := adapters.validator(ctx, *command.Slug, request, projectStringList(resolved.Config, "gate_paths"))
	if err != nil {
		return cli.writeError("environment", "project_config_invalid", "could not configure Shape validation", 3)
	}
	childEnvironment, err := shapeChildEnvironment(adapters.childEnv)
	if err != nil {
		return cli.writeError("controller", "shape_run_unavailable", "could not prepare Shape tool environment", 70)
	}
	checkoutRoot, err := safefs.OpenRoot(resolved.Checkout)
	if err != nil {
		return cli.writeError("environment", "shape_output_unavailable", "could not open project checkout", 3)
	}
	defer closeRoot(checkoutRoot)
	writePaths := [2]string{".kogen/intents/" + *command.Slug + "/intent.md", paths.Source}
	toolContext := &tools.ToolContext{
		Role: tools.RoleShaper, Workspace: resolved.Checkout, WorkspaceRoot: checkoutRoot,
		RunDir: runDir, RunRoot: runRoot, Process: ports.processes,
		Environment: childEnvironment, ShaperWritePaths: writePaths,
	}
	manifest := resolved.Roles
	factory := shapeConversationFactory(runID, cacheKey)
	toolExecutor := shapeToolExecutor{context: toolContext}
	progress := func(line string) { _, _ = io.WriteString(cli.Err, line+"\n") }
	runner, err := shaperun.New(shaperun.Options{
		Checkout: resolved.Checkout, Slug: *command.Slug, Request: request,
		RequestSource: *command.Request, RequestIssue: requestIssue,
		Domains: projectStringMapKeys(resolved.Config, "domains"), GatePaths: projectStringList(resolved.Config, "gate_paths"),
		Manifest: manifest, Factory: factory, Turner: adapters.provider, Tools: toolExecutor,
		AuditTransport: adapters.provider, Validation: validatorOptions,
		ScratchDir: scratchDir, ScratchRoot: scratchRoot, Roots: safefs.Opener{},
		StartedAt: startedAt, Progress: progress,
	})
	if err != nil {
		return cli.writeError("controller", "shape_run_unavailable", "could not construct Shape controller", 70)
	}
	result, err := runner.Execute(ctx)
	if err != nil {
		return cli.writeShapeFailure(err)
	}
	_, _ = io.WriteString(cli.Out, result.Text(*command.Slug))
	return 0
}

func shapeConversationFactory(runID, cacheKey string) shapesession.ConversationFactory {
	return func(spec shapesession.ConversationSpec) (*session.Conversation, error) {
		rung := "C1"
		if spec.Index == 2 {
			rung = "C2"
		}
		identity, err := session.Bind(session.Binding{
			RunID: runID, CacheKey: cacheKey, Role: spec.AssignedRole,
			Provider: spec.Settings.Provider, Model: spec.Settings.Model, Effort: spec.Settings.Effort,
			Stage: "shape", Attempt: "shaper", Rung: rung, Epoch: "initial",
		})
		if err != nil {
			return nil, err
		}
		identity.SessionID = cacheKey
		return session.New(identity)
	}
}

func readShapeRequest(input io.Reader, cwd, source string) ([]byte, string) {
	if source == "-" {
		contents, err := io.ReadAll(input)
		if err != nil {
			return nil, "unreadable"
		}
		if len(contents) == 0 {
			return nil, "empty"
		}
		return contents, ""
	}
	name := source
	if !filepath.IsAbs(name) {
		name = filepath.Join(cwd, name)
	}
	contents, err := os.ReadFile(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "not found"
		}
		return nil, "unreadable"
	}
	if len(contents) == 0 {
		return nil, "empty"
	}
	return contents, ""
}

func (cli *CLI) writeShapeFailure(err error) int {
	var failure *contract.Failure
	if errors.As(err, &failure) && failure != nil {
		code := int(failure.Exit)
		if code == 0 {
			code = 70
		}
		_, _ = io.WriteString(cli.Out, shaperun.FormatError(err)+"\n")
		return code
	}
	_, _ = io.WriteString(cli.Out, shaperun.FormatError(err)+"\n")
	return 70
}

type shapeToolExecutor struct{ context *tools.ToolContext }

func (executor shapeToolExecutor) Execute(ctx context.Context, calls []sse.ToolCall) ([]shaperun.ToolOutput, error) {
	outputs := make([]shaperun.ToolOutput, 0, len(calls))
	for _, call := range calls {
		output, err := executor.context.Execute(ctx, call.Name, call.Arguments)
		if err != nil {
			var expected *tools.ToolError
			if errors.As(err, &expected) {
				output = expected.Message
			} else {
				return nil, err
			}
		}
		outputs = append(outputs, shaperun.ToolOutput{CallID: call.ID, Output: output})
	}
	return outputs, nil
}

func projectStringList(config *project.Config, key string) []string {
	if config == nil {
		return nil
	}
	values, ok := config.Raw[key].(yamlmini.Sequence)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func projectStringMapKeys(config *project.Config, key string) []string {
	if config == nil {
		return nil
	}
	values, ok := config.Raw[key].(yamlmini.Mapping)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool { return strings.Compare(result[i], result[j]) < 0 })
	return result
}

func shapeChildEnvironment(environment process.Environment) ([]string, error) {
	return acceptance.SortedEnvironment(environment)
}
