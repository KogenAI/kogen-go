// Package app connects the public command parser to the production components.
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"kogen-go/internal/cli/parse"
	"kogen-go/internal/cli/render"
	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/process"
	"kogen-go/internal/project"
)

// CLI is one invocation of the public kogen command. Env, when supplied,
// replaces the process environment and makes command wiring deterministic in
// component tests.
type CLI struct {
	In   io.Reader
	Out  io.Writer
	Err  io.Writer
	CWD  string
	Env  process.Environment
	argv []string
}

// Run executes one public command with the process streams and environment.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return (&CLI{In: stdin, Out: stdout, Err: stderr}).Run(args)
}

// Run dispatches argv through the fixed parser before invoking a route.
func (cli *CLI) Run(args []string) int {
	cli.defaults()
	cli.argv = append(cli.argv[:0], args...)
	cwd := cli.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return cli.writeError("controller", "internal_error", "could not resolve the working directory", 70)
		}
	}
	request := parse.Parse(args, cwd)
	switch request.Kind {
	case parse.ResultHelp:
		_, _ = io.WriteString(cli.Out, request.Page.Contents())
		return 0
	case parse.ResultMoved:
		_, _ = io.WriteString(cli.Out, render.Moved(request.Message))
		return 2
	case parse.ResultUsage:
		_, _ = io.WriteString(cli.Out, (render.UsageError{Message: request.Message, Page: request.Page}).Render())
		return 2
	case parse.ResultCommand:
		return cli.runCommand(request.Command, cwd)
	default:
		return cli.writeError("controller", "internal_error", "the parser returned an unknown result", 70)
	}
}

func (cli *CLI) defaults() {
	if cli.In == nil {
		cli.In = os.Stdin
	}
	if cli.Out == nil {
		cli.Out = io.Discard
	}
	if cli.Err == nil {
		cli.Err = io.Discard
	}
	if cli.Env == nil {
		cli.Env = process.HostEnvironment()
	} else {
		copy := make(process.Environment, len(cli.Env))
		for key, value := range cli.Env {
			copy[key] = value
		}
		cli.Env = copy
	}
}

func (cli *CLI) runCommand(command parse.Command, cwd string) int {
	if command.Slug != nil && !validStatusSlug(*command.Slug) {
		return cli.writeError("intent", "invalid_slug", "Slug must use lowercase letters, digits, and dashes.", 2)
	}
	switch command.Route {
	case parse.RouteVersion:
		_, _ = io.WriteString(cli.Out, versionLine())
		return 0
	case parse.RouteStatus:
		return cli.status(command, cwd)
	case parse.RouteIntentApprove:
		return cli.approve(command, cwd)
	case parse.RouteIntentRemove:
		return cli.remove(command, cwd)
	case parse.RouteIntentShape:
		return cli.writeError("controller", "internal_error", "intent shaping is wired in the Shape integration round", 70)
	case parse.RouteQueueStart, parse.RouteQueueStop:
		return cli.writeError("controller", "internal_error", "queue execution is wired in the Build integration round", 70)
	case parse.RouteProviderList, parse.RouteProviderLogin, parse.RouteProviderLogout, parse.RouteProviderUse:
		return cli.writeError("controller", "internal_error", "provider commands are wired in the provider integration round", 70)
	default:
		return cli.writeError("controller", "internal_error", "command route is not available", 70)
	}
}

func versionLine() string {
	revision, date, modified := "unknown", "1970-01-01", false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.time":
				if len(setting.Value) >= len(date) {
					date = setting.Value[:len(date)]
				}
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
	}
	if len(revision) > 8 {
		revision = revision[:8]
	}
	if modified {
		return fmt.Sprintf("kogen %s (%s, uncommitted changes)\n", revision, date)
	}
	return fmt.Sprintf("kogen %s (%s)\n", revision, date)
}

type runtimePorts struct {
	env          process.Environment
	processes    contract.ProcessRunner
	workspaceGit contract.GitPort
	originGit    contract.GitPort
}

func (cli *CLI) ports() runtimePorts {
	return runtimePorts{
		env:          cli.Env,
		processes:    process.Supervisor{},
		workspaceGit: gitio.NewWorkspace(process.Supervisor{}),
		originGit:    gitio.NewOrigin(process.Supervisor{}),
	}
}

func (cli *CLI) resolveProject(ctx context.Context, command parse.Command, cwd string, ports runtimePorts) (*project.Resolution, int, string) {
	home := cli.Env["HOME"]
	if home == "" {
		home, _ = os.UserHomeDir()
		if home == "" {
			return nil, 3, "environment/home_unavailable: HOME is not set"
		}
	}
	options := project.Options{CWD: cwd, Home: home, Git: ports.workspaceGit}
	options.Provider = cli.Env["KOGEN_BENCH_PROVIDER"]
	if command.Project.Project != nil {
		options.Project = *command.Project.Project
	}
	if command.Project.Origin != nil && cli.hasOption("--origin") {
		options.Origin = *command.Project.Origin
	}
	if command.Project.Base != nil {
		options.Base = *command.Project.Base
	}
	options.Policy = func(directory string) contract.GitPolicy {
		return gitio.WorkspacePolicy(directory, ports.env)
	}
	resolved, err := project.Resolve(ctx, options)
	if err != nil {
		return nil, projectFailure(err, options), projectErrorLine(err, options)
	}
	return resolved, 0, ""
}

func (cli *CLI) hasOption(name string) bool {
	optionsEnded := false
	for index := 0; index < len(cli.argv); index++ {
		token := cli.argv[index]
		if optionsEnded {
			continue
		}
		if token == "--" {
			optionsEnded = true
			continue
		}
		option, _, attached := strings.Cut(token, "=")
		if option == name {
			return true
		}
		if !attached && (option == "--project" || option == "--origin" || option == "--base" || option == "--by" || option == "--as") {
			index++
		}
	}
	return false
}

func (cli *CLI) writeError(class, reason, detail string, exit int) int {
	message := class + "/" + reason
	if detail != "" {
		lines := strings.Split(strings.TrimSuffix(detail, "\n"), "\n")
		message += ": " + lines[0]
		for _, line := range lines[1:] {
			message += "\n  " + line
		}
	}
	_, _ = io.WriteString(cli.Out, message+"\n")
	return exit
}

func projectFailure(err error, options project.Options) int {
	var configErr *project.ConfigError
	if strings.Contains(err.Error(), "base unavailable") || strings.HasPrefix(err.Error(), "base unavailable:") {
		return 3
	}
	if strings.Contains(err.Error(), "not a Git work tree") || strings.Contains(err.Error(), "project unavailable") {
		return 3
	}
	if strings.Contains(err.Error(), "project Git port") || strings.Contains(err.Error(), "working directory") {
		return 70
	}
	if strings.Contains(err.Error(), "config") || asProjectConfigError(err, &configErr) {
		return 3
	}
	return 3
}

func projectErrorLine(err error, options project.Options) string {
	var configErr *project.ConfigError
	if asProjectConfigError(err, &configErr) {
		reason := "project_config_invalid"
		if filepath.Base(configErr.Path) == "config.yaml" {
			reason = "machine_config_invalid"
		}
		var detail strings.Builder
		for index, issue := range configErr.Issues {
			if index != 0 {
				detail.WriteByte('\n')
			}
			if issue.Line > 0 {
				fmt.Fprintf(&detail, "line %d: %s", issue.Line, issue.Detail)
			} else {
				detail.WriteString(issue.Detail)
			}
		}
		return "environment/" + reason + ": " + configErr.Path + "\n  " + strings.ReplaceAll(detail.String(), "\n", "\n  ")
	}
	message := err.Error()
	if strings.HasPrefix(message, "project unavailable: ") {
		return "environment/project_unavailable: " + strings.TrimPrefix(message, "project unavailable: ")
	}
	if strings.HasPrefix(message, "not a Git work tree: ") {
		return "environment/not_a_git_repo: " + strings.TrimPrefix(message, "not a Git work tree: ")
	}
	if strings.HasPrefix(message, "base unavailable:") {
		return "environment/base_unavailable: " + strings.TrimSpace(strings.TrimPrefix(message, "base unavailable:"))
	}
	if strings.Contains(message, "home directory is required") {
		return "environment/home_unavailable: HOME is not set"
	}
	return "environment/project_config_invalid: " + message
}

func asProjectConfigError(err error, target **project.ConfigError) bool {
	for err != nil {
		if value, ok := err.(*project.ConfigError); ok {
			*target = value
			return true
		}
		type unwrapper interface{ Unwrap() error }
		wrapped, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = wrapped.Unwrap()
	}
	return false
}
