package project

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"kogen-go/internal/yamlmini"
)

var projectKeys = []string{
	"name", "checks", "acceptance_checks", "setup", "setup_outputs", "setup_inputs", "fix",
	"format", "protected_paths", "gate_paths", "domains", "env", "sandbox", "base",
	"acceptance", "shaping", "build", "account",
}

var buildKeys = []string{
	"recipe", "roles", "wall_minutes", "edge_tests", "model_fallback", "context_bytes",
	"plan_max_words", "tool_result_tokens", "model_generation_tokens", "luna_provider_mode",
	"ladder", "land", "auditor_demotion", "budget_ms", "fallback",
}

var roleNames = []string{"builder", "planner", "shaper", "auditor", "reviewer", "context"}

func validateProject(root yamlmini.Mapping) []Issue {
	issues := make([]Issue, 0)
	unknownKeys(root, projectKeys, "project", &issues)
	for _, key := range []string{"name", "checks"} {
		if _, ok := root[key]; !ok {
			issues = append(issues, Issue{Detail: fmt.Sprintf("missing required key `%s`", key)})
		}
	}
	for _, key := range []string{"name", "base", "account"} {
		if value, ok := root[key]; ok {
			requireString(value, key, &issues)
		}
	}
	for _, field := range []string{"checks", "acceptance_checks", "setup", "fix"} {
		if value, ok := root[field]; ok {
			validateCheckList(value, field, &issues)
		}
	}
	for _, field := range []string{"setup_outputs", "setup_inputs"} {
		if value, ok := root[field]; ok {
			validateRelativePaths(value, field, field == "setup_outputs", &issues)
		}
	}
	for _, field := range []string{"protected_paths", "gate_paths"} {
		if value, ok := root[field]; ok {
			validateStringList(value, field, false, &issues)
		}
	}
	if value, ok := root["format"]; ok {
		validateStringList(value, "format", true, &issues)
	}
	if value, ok := root["domains"]; ok {
		validateDomains(value, &issues)
	}
	if value, ok := root["env"]; ok {
		validateEnv(value, &issues)
	}
	if value, ok := root["sandbox"]; ok && !boolScalar(value) {
		issues = append(issues, Issue{Detail: "sandbox must be true or false"})
	}
	if value, ok := root["acceptance"]; ok {
		validateAcceptance(value, &issues)
	}
	if value, ok := root["shaping"]; ok {
		validateShaping(value, &issues)
	}
	if value, ok := root["build"]; ok {
		build, valid := value.(yamlmini.Mapping)
		if !valid {
			issues = append(issues, Issue{Detail: "build must be a map"})
		} else {
			issues = append(issues, validateBuild(build)...)
		}
	}
	return issues
}

func validateBuild(build yamlmini.Mapping) []Issue {
	issues := make([]Issue, 0)
	unknownKeys(build, buildKeys, "build", &issues)
	if value, ok := build["recipe"]; ok {
		recipe, isString := value.(string)
		if !isString {
			issues = append(issues, Issue{Detail: "build.recipe must be a string"})
		} else if !validRecipe(recipe) {
			issues = append(issues, Issue{Detail: fmt.Sprintf("build.recipe must be one of the supported recipes; got %q", recipe)})
		}
	}
	if value, ok := build["roles"]; ok {
		validateRoles(value, &issues)
	}
	if value, ok := build["ladder"]; ok {
		validateLadder(value, &issues)
	}
	if value, ok := build["land"]; ok {
		if _, valid := value.(string); !valid {
			issues = append(issues, Issue{Detail: "build.land must be a string"})
		} else if value != "green" && value != "green-or-advisory" {
			issues = append(issues, Issue{Detail: "build.land must be green-or-advisory or green"})
		}
	}
	if value, ok := build["auditor_demotion"]; ok {
		if !boolScalar(value) {
			issues = append(issues, Issue{Detail: "build.auditor_demotion must be true or false"})
		} else if value == "true" {
			issues = append(issues, Issue{Detail: "build.auditor_demotion has no admitted calibration"})
		}
	}
	for _, field := range []string{"wall_minutes", "budget_ms", "context_bytes", "tool_result_tokens", "model_generation_tokens"} {
		if value, ok := build[field]; ok {
			if n, valid := positiveInteger(value); !valid || n == 0 {
				issues = append(issues, Issue{Detail: fmt.Sprintf("build.%s must be a positive integer", field)})
			}
		}
	}
	if value, ok := build["context_bytes"]; ok {
		if n, valid := positiveInteger(value); valid && n < 16000 {
			issues = append(issues, Issue{Detail: "build.context_bytes must be an integer of at least 16000"})
		}
	}
	if value, ok := build["plan_max_words"]; ok {
		n, valid := positiveInteger(value)
		if !valid || n < 300 || n > 2000 {
			issues = append(issues, Issue{Detail: "plan_max_words must be an integer from 300 to 2000"})
		}
	}
	if value, ok := build["tool_result_tokens"]; ok {
		n, valid := positiveInteger(value)
		if valid && (n < 128 || n > 100000) {
			issues = append(issues, Issue{Detail: "build.tool_result_tokens must be an integer from 128 to 100000"})
		}
	}
	if value, ok := build["model_generation_tokens"]; ok {
		n, valid := positiveInteger(value)
		if valid && n > 100000 {
			issues = append(issues, Issue{Detail: "build.model_generation_tokens must be an integer from 1 to 100000"})
		}
	}
	for _, field := range []string{"edge_tests", "model_fallback"} {
		if value, ok := build[field]; ok && !boolScalar(value) {
			issues = append(issues, Issue{Detail: fmt.Sprintf("build.%s must be true or false", field)})
		}
	}
	if value, ok := build["luna_provider_mode"]; ok {
		mode, isString := value.(string)
		if !isString || mode != "responses" && mode != "lite" {
			issues = append(issues, Issue{Detail: "build.luna_provider_mode must be responses or lite"})
		}
	}
	if value, ok := build["fallback"]; ok {
		validateFallback(value, &issues)
	}
	return issues
}

func validateRoles(value yamlmini.Value, issues *[]Issue) {
	roles, ok := value.(yamlmini.Mapping)
	if !ok {
		*issues = append(*issues, Issue{Detail: "build.roles must be a map"})
		return
	}
	known := make(map[string]struct{}, len(roleNames))
	for _, name := range roleNames {
		known[name] = struct{}{}
	}
	for _, role := range sortedKeys(roles) {
		if _, ok := known[role]; !ok {
			*issues = append(*issues, Issue{Detail: fmt.Sprintf("build.roles has unknown role %q", role)})
			continue
		}
		roleValue, ok := roles[role].(yamlmini.Mapping)
		if !ok {
			*issues = append(*issues, Issue{Detail: "build.roles." + role + " must be a map"})
			continue
		}
		unknownKeys(roleValue, []string{"model", "effort"}, "build.roles."+role, issues)
		for _, field := range []string{"model", "effort"} {
			if setting, exists := roleValue[field]; exists {
				requireString(setting, "build.roles."+role+"."+field, issues)
			}
		}
	}
}

func validateFallback(value yamlmini.Value, issues *[]Issue) {
	roles, ok := value.(yamlmini.Mapping)
	if !ok {
		*issues = append(*issues, Issue{Detail: "build.fallback must be a map"})
		return
	}
	for _, role := range sortedKeys(roles) {
		roleValue, ok := roles[role].(yamlmini.Mapping)
		if !ok {
			*issues = append(*issues, Issue{Detail: fmt.Sprintf("build.fallback.%s must be a map", role)})
			continue
		}
		unknownKeys(roleValue, []string{"model", "effort"}, "build.fallback."+role, issues)
		for _, field := range []string{"model", "effort"} {
			if setting, exists := roleValue[field]; exists {
				requireString(setting, "build.fallback."+role+"."+field, issues)
			} else {
				*issues = append(*issues, Issue{Detail: fmt.Sprintf("build.fallback.%s is missing required key `%s`", role, field)})
			}
		}
	}
}

func validateLadder(value yamlmini.Value, issues *[]Issue) {
	ladder, ok := value.(yamlmini.Mapping)
	if !ok {
		*issues = append(*issues, Issue{Detail: "build.ladder must be a map"})
		return
	}
	unknownKeys(ladder, []string{"max_rungs", "experimental_r4"}, "build.ladder", issues)
	if value, exists := ladder["max_rungs"]; exists {
		n, valid := positiveInteger(value)
		if !valid || n > 4 {
			*issues = append(*issues, Issue{Detail: "build.ladder.max_rungs must be an integer from 1 to 4"})
		}
	}
	if value, exists := ladder["experimental_r4"]; exists && !boolScalar(value) {
		*issues = append(*issues, Issue{Detail: "build.ladder.experimental_r4 must be true or false"})
	}
}

func validateCheckList(value yamlmini.Value, field string, issues *[]Issue) {
	rows, ok := value.(yamlmini.Sequence)
	if !ok {
		*issues = append(*issues, Issue{Detail: field + " must be a list"})
		return
	}
	names := make(map[string]struct{})
	for index, row := range rows {
		prefix := fmt.Sprintf("%s[%d]", field, index+1)
		item, ok := row.(yamlmini.Mapping)
		if !ok {
			*issues = append(*issues, Issue{Detail: prefix + " must be a map"})
			continue
		}
		unknownKeys(item, []string{"name", "argv", "timeout_ms"}, prefix, issues)
		if name, exists := item["name"]; exists {
			if text, ok := name.(string); !ok {
				*issues = append(*issues, Issue{Detail: prefix + ".name must be a string"})
			} else if _, duplicate := names[text]; duplicate {
				*issues = append(*issues, Issue{Detail: fmt.Sprintf("%s has duplicate name %q", field, text)})
			} else {
				names[text] = struct{}{}
			}
		} else {
			*issues = append(*issues, Issue{Detail: prefix + " is missing required key `name`"})
		}
		if argv, exists := item["argv"]; exists {
			args, ok := argv.(yamlmini.Sequence)
			if !ok {
				*issues = append(*issues, Issue{Detail: prefix + ".argv must be a list"})
			} else {
				valid := len(args) > 0
				for _, arg := range args {
					text, isString := arg.(string)
					if !isString || text == "" {
						valid = false
					}
				}
				if !valid {
					*issues = append(*issues, Issue{Detail: prefix + ".argv must be a non-empty list of strings"})
				}
			}
		} else {
			*issues = append(*issues, Issue{Detail: prefix + " is missing required key `argv`"})
		}
		if timeout, exists := item["timeout_ms"]; exists {
			n, valid := positiveInteger(timeout)
			if !valid || n == 0 {
				*issues = append(*issues, Issue{Detail: prefix + ".timeout_ms must be a positive integer"})
			}
		} else {
			*issues = append(*issues, Issue{Detail: prefix + " is missing required key `timeout_ms`"})
		}
	}
}

func validateRelativePaths(value yamlmini.Value, field string, output bool, issues *[]Issue) {
	rows, ok := value.(yamlmini.Sequence)
	if !ok {
		*issues = append(*issues, Issue{Detail: field + " must be a list"})
		return
	}
	seen := make([]string, 0, len(rows))
	for _, row := range rows {
		text, ok := row.(string)
		if !ok {
			*issues = append(*issues, Issue{Detail: field + " must contain only strings"})
			return
		}
		if output {
			bad := text == "" || text == "." || strings.ContainsAny(text, "\x00\r\n") || filepathIsAbs(text)
			for _, part := range strings.Split(strings.ReplaceAll(text, "\\", "/"), "/") {
				if part == ".." || part == "." || part == ".git" {
					bad = true
				}
			}
			if bad {
				*issues = append(*issues, Issue{Detail: field + " entries must be safe relative paths"})
			}
		}
		seen = append(seen, text)
	}
	if !output {
		return
	}
	for i, current := range seen {
		for _, previous := range seen[:i] {
			if previous == current || strings.HasPrefix(current, strings.TrimSuffix(previous, "/")+"/") || strings.HasPrefix(previous, strings.TrimSuffix(current, "/")+"/") {
				*issues = append(*issues, Issue{Detail: field + " has duplicate or overlapping paths"})
				return
			}
		}
	}
}

func filepathIsAbs(value string) bool {
	return strings.HasPrefix(value, "/") || len(value) > 1 && value[1] == ':'
}

func validateStringList(value yamlmini.Value, field string, nonempty bool, issues *[]Issue) {
	rows, ok := value.(yamlmini.Sequence)
	if !ok {
		*issues = append(*issues, Issue{Detail: field + " must be a list"})
		return
	}
	for _, row := range rows {
		text, ok := row.(string)
		if !ok || nonempty && text == "" {
			*issues = append(*issues, Issue{Detail: field + " must contain only strings"})
			return
		}
	}
}

func validateDomains(value yamlmini.Value, issues *[]Issue) {
	domains, ok := value.(yamlmini.Mapping)
	if !ok {
		*issues = append(*issues, Issue{Detail: "domains must be a map"})
		return
	}
	for _, name := range sortedKeys(domains) {
		rows, ok := domains[name].(yamlmini.Sequence)
		if !ok {
			*issues = append(*issues, Issue{Detail: fmt.Sprintf("domains.%s must be a list of strings", name)})
			continue
		}
		for _, row := range rows {
			if _, ok := row.(string); !ok {
				*issues = append(*issues, Issue{Detail: fmt.Sprintf("domains.%s must be a list of strings", name)})
				break
			}
		}
	}
}

func validateEnv(value yamlmini.Value, issues *[]Issue) {
	env, ok := value.(yamlmini.Mapping)
	if !ok {
		*issues = append(*issues, Issue{Detail: "env must be a map"})
		return
	}
	for _, name := range sortedKeys(env) {
		if !validEnvName(name) || strings.HasPrefix(name, "KOGEN_") {
			*issues = append(*issues, Issue{Detail: fmt.Sprintf("env key %q is not allowed", name)})
		}
		if _, ok := env[name].(string); !ok {
			*issues = append(*issues, Issue{Detail: fmt.Sprintf("env.%s must be a string", name)})
		}
	}
}

func validateAcceptance(value yamlmini.Value, issues *[]Issue) {
	acceptance, ok := value.(yamlmini.Mapping)
	if !ok {
		*issues = append(*issues, Issue{Detail: "acceptance must be a map"})
		return
	}
	unknownKeys(acceptance, []string{"adapter", "ext", "candidate_dir", "run", "timeout_ms"}, "acceptance", issues)
	for _, field := range []string{"adapter", "ext", "candidate_dir"} {
		if item, exists := acceptance[field]; exists {
			requireString(item, "acceptance."+field, issues)
		}
	}
	if run, exists := acceptance["run"]; exists {
		validateStringList(run, "acceptance.run", true, issues)
	}
	if timeout, exists := acceptance["timeout_ms"]; exists {
		n, valid := positiveInteger(timeout)
		if !valid || n == 0 {
			*issues = append(*issues, Issue{Detail: "acceptance.timeout_ms must be a positive integer"})
		}
	}
}

func validateShaping(value yamlmini.Value, issues *[]Issue) {
	shaping, ok := value.(yamlmini.Mapping)
	if !ok {
		*issues = append(*issues, Issue{Detail: "shaping must be a map"})
		return
	}
	unknownKeys(shaping, []string{"proof"}, "shaping", issues)
	if value, exists := shaping["proof"]; exists {
		proof, isString := value.(string)
		if !isString || proof != "none" && proof != "witness" {
			*issues = append(*issues, Issue{Detail: "shaping.proof must be none or witness"})
		}
	}
}

func unknownKeys(mapping yamlmini.Mapping, allowed []string, prefix string, issues *[]Issue) {
	known := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		known[key] = struct{}{}
	}
	for _, key := range sortedKeys(mapping) {
		if _, ok := known[key]; !ok {
			*issues = append(*issues, Issue{Detail: fmt.Sprintf("%s has unknown key %q", prefix, key)})
		}
	}
}

func requireString(value yamlmini.Value, field string, issues *[]Issue) {
	if _, ok := value.(string); !ok {
		*issues = append(*issues, Issue{Detail: field + " must be a string"})
	}
}

func scalar(value yamlmini.Value) string {
	text, _ := value.(string)
	return text
}

func boolScalar(value yamlmini.Value) bool {
	text, ok := value.(string)
	return ok && (text == "true" || text == "false")
}

func positiveInteger(value yamlmini.Value) (uint64, bool) {
	text, ok := value.(string)
	if !ok || text == "" || strings.TrimSpace(text) != text || strings.HasPrefix(text, "+") || strings.HasPrefix(text, "-") {
		return 0, false
	}
	n, err := strconv.ParseUint(text, 10, 64)
	return n, err == nil
}

func validEnvName(value string) bool {
	for index, r := range value {
		if index == 0 {
			if r != '_' && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') {
				return false
			}
		} else if r != '_' && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return value != ""
}

func validRecipe(value string) bool {
	const suffix = "+edge"
	base := strings.TrimSuffix(value, suffix)
	known := map[string]struct{}{
		"ladder": {}, "ladder-diverse": {}, "ladder-luna": {}, "ladder-sol-low": {},
		"ladder-sol-medium": {}, "ladder-sol-high": {}, "plan-shell": {}, "staged": {},
		"direct": {}, "direct-escalate": {}, "direct-shell": {}, "escalate-shell": {},
	}
	_, ok := known[base]
	return ok && (base == value || strings.HasSuffix(value, suffix))
}

func sortedKeys(mapping yamlmini.Mapping) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
