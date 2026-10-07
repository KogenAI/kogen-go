package gitio

import (
	"sort"
	"strings"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/process"
)

const (
	GitTimeout       = 120 * time.Second
	GitOutputLimit   = process.MaximumOutputLimit - 1
	GitStderrTailCap = process.OutputTailBytes
)

// WorkspacePolicy creates the fixed, hermetic policy for Git operations in a
// controlled workspace. The caller supplies the controller environment; this
// function copies it and never merges project-child environment values.
func WorkspacePolicy(directory string, base process.Environment) contract.GitPolicy {
	env := process.ControllerGitEnvironment(base)
	env["GIT_CONFIG_GLOBAL"] = "/dev/null"
	env["GIT_CONFIG_NOSYSTEM"] = "1"
	env["GIT_ATTR_NOSYSTEM"] = "1"
	env["GIT_TERMINAL_PROMPT"] = "0"
	env["GIT_PAGER"] = "cat"
	env["PAGER"] = "cat"
	for key := range env {
		if workspaceHelperEnvironment(key) {
			delete(env, key)
		}
	}
	return policy(directory, env)
}

// OriginPolicy creates the fixed policy for operations on the user's origin.
// Global Git identity and signing configuration remain available. The runner
// pins helper settings from that global configuration so repository-local
// config cannot replace them with workspace-selected programs.
func OriginPolicy(directory string, base process.Environment) contract.GitPolicy {
	env := process.ControllerGitEnvironment(base)
	env["GIT_CONFIG_NOSYSTEM"] = "1"
	env["GIT_ATTR_NOSYSTEM"] = "1"
	env["GIT_TERMINAL_PROMPT"] = "0"
	env["GIT_PAGER"] = "cat"
	env["PAGER"] = "cat"
	deleteGitConfigEnvironment(env)
	return policy(directory, env)
}

func policy(directory string, env process.Environment) contract.GitPolicy {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, key+"="+env[key])
	}
	return contract.GitPolicy{
		WorkingDirectory: directory,
		Environment:      entries,
		Timeout:          GitTimeout,
		StdoutLimit:      GitOutputLimit,
		StderrTailLimit:  GitStderrTailCap,
	}
}

func workspaceHelperEnvironment(key string) bool {
	switch key {
	case "GIT_CONFIG_PARAMETERS", "GIT_EXEC_PATH", "GIT_SSH", "GIT_SSH_COMMAND",
		"GIT_ASKPASS", "GIT_EXTERNAL_DIFF", "GIT_TRACE", "GIT_TRACE_SETUP",
		"GIT_TRACE_PACKET", "GIT_TRACE_PERFORMANCE", "GIT_TRACE2", "GIT_TRACE2_EVENT",
		"GIT_TRACE2_PERF", "SSH_ASKPASS":
		return true
	default:
		return false
	}
}

func deleteGitConfigEnvironment(env process.Environment) {
	for key := range env {
		if key == "GIT_CONFIG_PARAMETERS" || key == "GIT_CONFIG_COUNT" ||
			strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			delete(env, key)
		}
	}
}
