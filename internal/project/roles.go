package project

import (
	"fmt"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/yamlmini"
)

// ResolveRoles merges project role fields over machine role fields over
// provider-specific defaults. fallback_shaper is then copied from the fully
// resolved shaper and cannot be overridden independently.
func ResolveRoles(provider string, project *Config, machine *MachineConfig) (contract.RoleManifest, error) {
	if provider == "" {
		provider = "chatgpt"
	}
	if provider != "chatgpt" && provider != "grok" {
		return contract.RoleManifest{}, fmt.Errorf("unsupported provider %q", provider)
	}
	manifest := contract.RoleManifest{Effective: make(map[contract.RoleName]contract.RoleSettings, len(roleNames))}
	for _, role := range roleNames {
		model, effort := defaultRole(provider, role)
		if machine != nil {
			applyRole(machine.Build, role, &model, &effort)
		}
		if project != nil {
			applyRole(mapping(project.Raw["build"]), role, &model, &effort)
		}
		if actual := modelProvider(model); actual != "" && actual != provider {
			return contract.RoleManifest{}, &ConfigError{
				Path:   projectPath(project),
				Issues: []Issue{{Detail: fmt.Sprintf("build.roles.%s.model selects %s under the %s provider", role, actual, provider)}},
			}
		}
		manifest.Effective[contract.RoleName(role)] = contract.RoleSettings{
			Provider: provider,
			Model:    model,
			Effort:   effort,
		}
	}
	manifest.FallbackShaper = manifest.Effective[contract.RoleName("shaper")]
	return manifest, nil
}

// LandPolicy resolves the project value before the machine value. The v1.3
// default is green; green-or-advisory remains an accepted spelling with the
// same eligibility while auditor demotion is disabled.
func LandPolicy(project *Config, machine *MachineConfig) string {
	if value := buildScalar(projectBuild(project), "land"); value != "" {
		return value
	}
	if value := buildScalar(machineBuild(machine), "land"); value != "" {
		return value
	}
	return "green"
}

// AuditorDemotion reports the validated experimental setting. True cannot be
// returned by valid configs in the current profile because schema validation
// refuses it until a calibration is admitted.
func AuditorDemotion(project *Config, machine *MachineConfig) bool {
	if value := buildScalar(projectBuild(project), "auditor_demotion"); value != "" {
		return value == "true"
	}
	return buildScalar(machineBuild(machine), "auditor_demotion") == "true"
}

func defaultRole(provider, role string) (string, string) {
	if provider == "grok" {
		return "grok-4.6", "high"
	}
	if role == "builder" {
		return "gpt-6-luna", "max"
	}
	return "gpt-6.1-sol", "high"
}

func applyRole(build yamlmini.Mapping, name string, model, effort *string) {
	roles := mapping(build["roles"])
	settings := mapping(roles[name])
	if value, ok := settings["model"].(string); ok {
		*model = value
	}
	if value, ok := settings["effort"].(string); ok {
		*effort = value
	}
}

func modelProvider(model string) string {
	switch {
	case strings.HasPrefix(model, "grok-"):
		return "grok"
	case strings.HasPrefix(model, "gpt-"):
		return "chatgpt"
	default:
		return ""
	}
}

func projectPath(project *Config) string {
	if project == nil || project.sourcePath == "" {
		return ".kogen/project.yaml"
	}
	return project.sourcePath
}

func projectBuild(project *Config) yamlmini.Mapping {
	if project == nil {
		return nil
	}
	return mapping(project.Raw["build"])
}

func machineBuild(machine *MachineConfig) yamlmini.Mapping {
	if machine == nil {
		return nil
	}
	return machine.Build
}

func buildScalar(build yamlmini.Mapping, key string) string {
	value, _ := build[key].(string)
	return value
}

func mapping(value yamlmini.Value) yamlmini.Mapping {
	result, _ := value.(yamlmini.Mapping)
	return result
}
