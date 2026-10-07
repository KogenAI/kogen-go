package parse

import (
	"path/filepath"
	"strings"
)

type optionValues struct {
	project *string
	origin  *string
	base    *string
	by      *string
	asLabel *string
	watch   bool
	json    bool
	force   bool
	detach  bool

	booleanValue  string
	unknownOption string
	disallowed    string
	missingValue  string
}

func scanOptions(route Route, tail []string) optionValues {
	var options optionValues
	optionsEnded := false
	for i := 0; i < len(tail); i++ {
		token := tail[i]
		if optionsEnded || token == "-" || token == "" || token[0] != '-' {
			continue
		}
		if token == "--" {
			optionsEnded = true
			continue
		}

		name, attached, hasAttached := splitOption(token)
		switch name {
		case "--json", "--watch", "--force", "--detach":
			if hasAttached {
				if options.booleanValue == "" {
					options.booleanValue = name
				}
				continue
			}
			if !optionAllowed(route, name) {
				if options.disallowed == "" {
					options.disallowed = name
				}
				continue
			}
			switch name {
			case "--json":
				options.json = true
			case "--watch":
				options.watch = true
			case "--force":
				options.force = true
			case "--detach":
				options.detach = true
			}
		case "--project", "--origin", "--base", "--by", "--as":
			if !optionAllowed(route, name) && options.disallowed == "" {
				options.disallowed = name
			}
			var value *string
			if hasAttached {
				value = stringPointer(attached)
			} else if i+1 < len(tail) && tail[i+1] != "--" {
				i++
				value = stringPointer(tail[i])
			} else if options.missingValue == "" {
				options.missingValue = name
			}
			if value != nil {
				switch name {
				case "--project":
					options.project = value
				case "--origin":
					options.origin = value
				case "--base":
					options.base = value
				case "--by":
					options.by = value
				case "--as":
					options.asLabel = value
				}
			}
		default:
			if options.unknownOption == "" {
				options.unknownOption = name
			}
		}
	}
	return options
}

func splitOption(token string) (name, attached string, hasAttached bool) {
	for i := 0; i < len(token); i++ {
		if token[i] == '=' {
			return token[:i], token[i+1:], true
		}
	}
	return token, "", false
}

func optionAllowed(route Route, option string) bool {
	switch route {
	case RouteStatus:
		return oneOf(option, "--project", "--origin", "--base", "--watch", "--json")
	case RouteIntentShape:
		return oneOf(option, "--project", "--origin", "--base")
	case RouteIntentApprove:
		return oneOf(option, "--project", "--origin", "--base", "--by")
	case RouteIntentRemove:
		return oneOf(option, "--project", "--origin", "--base", "--force")
	case RouteQueueStart:
		return oneOf(option, "--project", "--origin", "--base", "--detach")
	case RouteQueueStop:
		return oneOf(option, "--project", "--origin", "--base")
	case RouteProviderUse:
		return oneOf(option, "--project", "--as")
	default:
		return false
	}
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func collectPositionals(tail []string) []string {
	positionals := make([]string, 0, len(tail))
	optionsEnded := false
	for i := 0; i < len(tail); i++ {
		token := tail[i]
		if optionsEnded {
			positionals = append(positionals, token)
			continue
		}
		if token == "--" {
			optionsEnded = true
			continue
		}
		if token == "-" || len(token) == 0 || token[0] != '-' {
			positionals = append(positionals, token)
			continue
		}
		name, _, hasAttached := splitOption(token)
		if isValueOption(name) && !hasAttached && i+1 < len(tail) && tail[i+1] != "--" {
			i++
		}
	}
	return positionals
}

func isValueOption(name string) bool {
	return oneOf(name, "--project", "--origin", "--base", "--by", "--as")
}

func missingPositional(route Route, count int) string {
	switch route {
	case RouteIntentShape:
		if count < 1 {
			return "<slug>"
		}
		if count < 2 {
			return "<file|->"
		}
	case RouteIntentApprove, RouteIntentRemove:
		if count < 1 {
			return "<slug>"
		}
	case RouteProviderLogin, RouteProviderLogout, RouteProviderUse:
		if count < 1 {
			return "<provider>"
		}
	}
	return ""
}

func maxPositionals(route Route) int {
	switch route {
	case RouteStatus, RouteIntentRemove, RouteProviderLogin, RouteProviderLogout, RouteProviderUse:
		return 1
	case RouteIntentShape, RouteIntentApprove:
		return 2
	default:
		return 0
	}
}

func buildCommand(route Route, values []string, options optionValues, cwd string) (Command, string) {
	command := Command{Route: route}
	switch route {
	case RouteStatus:
		command.Slug = positional(values, 0)
		command.Watch, command.JSON = options.watch, options.json
		command.Project = defaultProjectOptions(options, cwd)
		if options.watch && options.json {
			return Command{}, "kogen status: --watch and --json can't be combined"
		}
	case RouteIntentShape:
		command.Slug, command.Request = positional(values, 0), positional(values, 1)
		command.Project = defaultProjectOptions(options, cwd)
	case RouteIntentApprove:
		command.Slug, command.Hash, command.By = positional(values, 0), positional(values, 1), options.by
		command.Project = defaultProjectOptions(options, cwd)
		if command.Hash != nil && !validHashPrefix(*command.Hash) {
			return Command{}, "kogen intent approve: <hash> must be 6 to 64 lowercase hex characters"
		}
	case RouteIntentRemove:
		command.Slug, command.Force = positional(values, 0), options.force
		command.Project = defaultProjectOptions(options, cwd)
	case RouteQueueStart:
		command.Detach = options.detach
		command.Project = defaultProjectOptions(options, cwd)
	case RouteQueueStop:
		command.Project = defaultProjectOptions(options, cwd)
	case RouteProviderLogin, RouteProviderLogout, RouteProviderUse:
		command.Provider = values[0]
		if !oneOf(command.Provider, "chatgpt", "grok") {
			verb := "login"
			if route == RouteProviderLogout {
				verb = "logout"
			} else if route == RouteProviderUse {
				verb = "use"
			}
			return Command{}, "kogen provider " + verb + ": unknown provider '" + command.Provider + "' (supported: chatgpt, grok)"
		}
		if route == RouteProviderUse {
			if options.asLabel == nil {
				return Command{}, "kogen provider use: missing --as <label>"
			}
			command.Label = options.asLabel
			command.Project = ProjectOptions{Project: optionalAbsolutePathPointer(options.project, cwd)}
		}
	case RouteProviderList, RouteVersion:
		// These leaves have no command inputs.
	}
	return command, ""
}

func positional(values []string, index int) *string {
	if index >= len(values) {
		return nil
	}
	return stringPointer(values[index])
}

func defaultProjectOptions(options optionValues, cwd string) ProjectOptions {
	return ProjectOptions{
		Project: absolutePathPointer(options.project, cwd),
		Origin:  absolutePathPointer(options.origin, cwd),
		Base:    cloneString(options.base),
	}
}

func absolutePathPointer(value *string, cwd string) *string {
	if value == nil {
		return stringPointer(absolutePath(cwd, nil))
	}
	return stringPointer(absolutePath(cwd, value))
}

func optionalAbsolutePathPointer(value *string, cwd string) *string {
	if value == nil {
		return nil
	}
	return stringPointer(absolutePath(cwd, value))
}

func absolutePath(cwd string, value *string) string {
	base, err := filepath.Abs(cwd)
	if err != nil {
		base = filepath.Clean(cwd)
	}
	if value == nil {
		return base
	}
	if filepath.IsAbs(*value) {
		return *value
	}
	return strings.TrimSuffix(base, string(filepath.Separator)) + string(filepath.Separator) + *value
}

func validHashPrefix(hash string) bool {
	if len(hash) < 6 || len(hash) > 64 {
		return false
	}
	for i := 0; i < len(hash); i++ {
		if !(hash[i] >= '0' && hash[i] <= '9') && !(hash[i] >= 'a' && hash[i] <= 'f') {
			return false
		}
	}
	return true
}

func stringPointer(value string) *string { return &value }

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	return stringPointer(*value)
}
