// Package rails defines project detection and command policy for the built-in
// Rails acceptance adapter. The helpers are pure except for Detected, which
// inspects the two files that identify a Rails checkout.
package rails

import (
	"errors"
	"os"
	"path"
	"regexp"
	"strings"

	"kogen-go/internal/process"
)

const (
	// AcceptanceExtension is appended to a validated Intent slug.
	AcceptanceExtension = "_test.rb"
	// CandidateDirectory contains the staged acceptance test in a Build tree.
	CandidateDirectory = "test/acceptance"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

var gateFiles = []string{"Gemfile", "Gemfile.lock", "bin/rails", ".standard.yml", ".rubocop.yml"}
var setupSeeds = []string{"vendor/cache"}

// Detected reports whether checkout has both Rails marker files. A single
// Gemfile or application file is not enough to select the Rails adapter.
func Detected(checkout string) bool {
	return regularFile(path.Join(checkout, "Gemfile")) && regularFile(path.Join(checkout, "config/application.rb"))
}

// Selected resolves explicit adapter configuration before detection. A nil
// configuredAdapter enables automatic detection; a non-nil value is an
// explicit override, including when it names a different adapter.
func Selected(checkout string, configuredAdapter *string) bool {
	if configuredAdapter != nil {
		return *configuredAdapter == "rails"
	}
	return Detected(checkout)
}

// SourcePath returns the Rails acceptance source path for a validated Intent
// slug. It rejects malformed slugs so callers cannot form paths outside the
// Kogen acceptance directory.
func SourcePath(slug string) (string, error) {
	if err := validateSlug(slug); err != nil {
		return "", err
	}
	return path.Join(".kogen", "acceptance", slug+AcceptanceExtension), nil
}

// CandidatePath returns the Rails test path used in the candidate tree.
func CandidatePath(slug string) (string, error) {
	if err := validateSlug(slug); err != nil {
		return "", err
	}
	return path.Join(CandidateDirectory, slug+AcceptanceExtension), nil
}

// RunnerCommand returns the configured Rails test invocation. The acceptance
// runner replaces the exact {path} argument with the staged candidate path.
func RunnerCommand() []string {
	return []string{"bundle", "exec", "rails", "test", "{path}"}
}

// AcceptanceCheck returns the Ruby syntax check for a staged test file.
func AcceptanceCheck() []string {
	return []string{"ruby", "-c", "{path}"}
}

// Formatter selects the Rails project's formatter from literal Gemfile gem
// declarations. StandardRB takes precedence when both supported gems appear.
func Formatter(gemfile string) []string {
	switch {
	case declaresGem(gemfile, "standard"):
		return []string{"bundle", "exec", "standardrb", "-a"}
	case declaresGem(gemfile, "rubocop"):
		return []string{"bundle", "exec", "rubocop", "-a"}
	default:
		return nil
	}
}

// SetupCommand returns the offline Bundler installation command. No network
// fallback is permitted by this adapter.
func SetupCommand() []string {
	return []string{"bundle", "install", "--local"}
}

// SetupSeeds lists project data copied into a fresh workspace before setup.
func SetupSeeds() []string {
	return append([]string(nil), setupSeeds...)
}

// ChildEnvironment returns Rails-specific variables to merge into the
// already-filtered child environment. vendorCache is the workspace's vendor
// cache path and becomes Bundler's local install path.
func ChildEnvironment(vendorCache string) process.Environment {
	return process.Environment{"BUNDLE_PATH": vendorCache, "RAILS_ENV": "test"}
}

// GateFiles lists Rails inputs whose changes invalidate the project gate.
func GateFiles() []string {
	return append([]string(nil), gateFiles...)
}

func validateSlug(slug string) error {
	if len(slug) < 3 || len(slug) > 48 || !slugPattern.MatchString(slug) {
		return errors.New("rails: invalid Intent slug")
	}
	return nil
}

func regularFile(name string) bool {
	info, err := os.Stat(name)
	return err == nil && info.Mode().IsRegular()
}

func declaresGem(gemfile, expected string) bool {
	for _, line := range strings.Split(gemfile, "\n") {
		if lineDeclaresGem(line, expected) {
			return true
		}
	}
	return false
}

// lineDeclaresGem scans Ruby tokens without executing Gemfile code. It ignores
// comments and string contents and accepts the two ordinary literal forms:
// gem "name" and gem("name").
func lineDeclaresGem(line, expected string) bool {
	for index := 0; index < len(line); {
		switch line[index] {
		case '#':
			return false
		case '\'', '"':
			quote := line[index]
			index++
			for index < len(line) {
				if line[index] == '\\' {
					index += 2
					if index > len(line) {
						index = len(line)
					}
				} else if line[index] == quote {
					index++
					break
				} else {
					index++
				}
			}
		case 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z', 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 'K', 'L', 'M', 'N', 'O', 'P', 'Q', 'R', 'S', 'T', 'U', 'V', 'W', 'X', 'Y', 'Z', '_':
			start := index
			index++
			for index < len(line) && identifierByte(line[index]) {
				index++
			}
			if line[start:index] == "gem" && declarationArgument(line[index:]) == expected {
				return true
			}
		default:
			index++
		}
	}
	return false
}

func identifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}

func declarationArgument(tail string) string {
	tail = strings.TrimLeft(tail, " \t")
	if strings.HasPrefix(tail, "(") {
		tail = strings.TrimLeft(tail[1:], " \t")
	}
	if len(tail) == 0 || (tail[0] != '\'' && tail[0] != '"') {
		return ""
	}
	quote := tail[0]
	literal := tail[1:]
	end := strings.IndexByte(literal, quote)
	if end < 0 {
		return ""
	}
	return literal[:end]
}
