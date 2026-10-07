package intent

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"kogen-go/internal/yamlmini"
)

// Contract is a named path predicate used for assumptions and shared
// contracts.
type Contract struct {
	Name     string
	Path     string
	Contains string
}

// Frontmatter is the typed subset of the Intent YAML header.
type Frontmatter struct {
	Title           string
	Size            string
	Domains         []string
	ChangesGate     bool
	Limits          []string
	BlocksOn        []string
	Priority        int64
	Assumptions     []Contract
	SharedContracts []Contract
	Source          *string
}

func parseFrontmatter(source []byte) (Frontmatter, error) {
	value, err := yamlmini.Parse(source)
	if err != nil {
		if issue, ok := err.(*yamlmini.Issue); ok {
			return Frontmatter{}, parseError(issue.Line+1, issue.Message)
		}
		return Frontmatter{}, parseError(2, err.Error())
	}
	root, ok := value.(yamlmini.Mapping)
	if !ok {
		return Frontmatter{}, parseError(2, "frontmatter must be a YAML map")
	}

	allowed := map[string]bool{
		"title": true, "size": true, "domains": true, "changes_gate": true,
		"limits": true, "blocks_on": true, "priority": true,
		"assumptions": true, "shared_contracts": true, "source": true,
	}
	var issues []*ParseError
	unknown := make([]string, 0)
	for key := range root {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	sort.Slice(unknown, func(i, j int) bool {
		left, right := frontmatterLine(source, unknown[i]), frontmatterLine(source, unknown[j])
		if left == right {
			return unknown[i] < unknown[j]
		}
		return left < right
	})
	for _, key := range unknown {
		issues = append(issues, parseError(frontmatterLine(source, key), fmt.Sprintf("unknown frontmatter key %q", key)))
	}
	for _, key := range []string{"title", "size", "domains"} {
		if _, exists := root[key]; !exists {
			issues = append(issues, parseError(2, "frontmatter is missing required key `"+key+"`"))
		}
	}

	var result Frontmatter
	result.Title = stringField(root, source, "title", &issues)
	result.Size = stringField(root, source, "size", &issues)
	result.Domains = stringListField(root, source, "domains", &issues)
	result.Limits = stringListField(root, source, "limits", &issues)
	result.BlocksOn = stringListField(root, source, "blocks_on", &issues)
	result.Assumptions = contractListField(root, source, "assumptions", &issues)
	result.SharedContracts = contractListField(root, source, "shared_contracts", &issues)

	if value, exists := root["changes_gate"]; exists {
		text, isString := value.(string)
		if !isString || (text != "true" && text != "false") {
			issues = append(issues, parseError(frontmatterLine(source, "changes_gate"), "frontmatter `changes_gate` must be true or false"))
		} else {
			result.ChangesGate = text == "true"
		}
	}
	if value, exists := root["priority"]; exists {
		text, isString := value.(string)
		priority, parseErr := strconv.ParseInt(text, 10, 64)
		if !isString || parseErr != nil {
			issues = append(issues, parseError(frontmatterLine(source, "priority"), "frontmatter `priority` must be an integer"))
		} else {
			result.Priority = priority
		}
	}
	if value, exists := root["source"]; exists {
		text, isString := value.(string)
		if !isString {
			issues = append(issues, parseError(frontmatterLine(source, "source"), "frontmatter `source` must be a string"))
		} else {
			result.Source = &text
		}
	}
	if len(issues) > 0 {
		first := issues[0]
		for _, issue := range issues[1:] {
			if issue.Line < first.Line {
				first = issue
			}
		}
		return Frontmatter{}, first
	}
	return result, nil
}

func stringField(root yamlmini.Mapping, source []byte, key string, issues *[]*ParseError) string {
	value, exists := root[key]
	text, ok := value.(string)
	if !exists || !ok {
		*issues = append(*issues, parseError(frontmatterLine(source, key), fmt.Sprintf("frontmatter `%s` must be a string", key)))
		return ""
	}
	return text
}

func stringListField(root yamlmini.Mapping, source []byte, key string, issues *[]*ParseError) []string {
	value, exists := root[key]
	if !exists {
		return nil
	}
	sequence, ok := value.(yamlmini.Sequence)
	if !ok {
		*issues = append(*issues, parseError(frontmatterLine(source, key), fmt.Sprintf("frontmatter `%s` must be a list", key)))
		return nil
	}
	values := make([]string, 0, len(sequence))
	for _, entry := range sequence {
		text, ok := entry.(string)
		if !ok {
			*issues = append(*issues, parseError(frontmatterLine(source, key), fmt.Sprintf("frontmatter `%s` must contain only strings", key)))
			return nil
		}
		values = append(values, text)
	}
	return values
}

func contractListField(root yamlmini.Mapping, source []byte, key string, issues *[]*ParseError) []Contract {
	value, exists := root[key]
	if !exists {
		return nil
	}
	sequence, ok := value.(yamlmini.Sequence)
	if !ok {
		*issues = append(*issues, parseError(frontmatterLine(source, key), fmt.Sprintf("frontmatter `%s` must be a list", key)))
		return nil
	}
	contracts := make([]Contract, 0, len(sequence))
	for _, entry := range sequence {
		mapping, ok := entry.(yamlmini.Mapping)
		if !ok {
			*issues = append(*issues, parseError(frontmatterLine(source, key), fmt.Sprintf("frontmatter `%s` must contain only maps", key)))
			return nil
		}
		contract := Contract{}
		valid := true
		for _, required := range []struct {
			name   string
			target *string
		}{
			{name: "name", target: &contract.Name},
			{name: "path", target: &contract.Path},
			{name: "contains", target: &contract.Contains},
		} {
			field, target := required.name, required.target
			text, ok := mapping[field].(string)
			if !ok {
				*issues = append(*issues, parseError(frontmatterLine(source, key), fmt.Sprintf("frontmatter `%s` entries require string `%s`", key, field)))
				valid = false
				break
			}
			*target = text
		}
		if valid {
			contracts = append(contracts, contract)
		}
	}
	return contracts
}

func frontmatterLine(source []byte, key string) int {
	lines := bytesSplitLines(source)
	forms := []string{key, fmt.Sprintf("%q", key), "'" + key + "'"}
	for index, line := range lines {
		if strings.TrimLeft(line, " ") != line {
			continue
		}
		for _, form := range forms {
			if tail, ok := strings.CutPrefix(line, form); ok && strings.HasPrefix(strings.TrimLeft(tail, " \t"), ":") {
				return index + 2 // the first frontmatter line is Intent line 2
			}
		}
	}
	return 2
}

func bytesSplitLines(source []byte) []string {
	lines := strings.Split(string(source), "\n")
	for index, line := range lines {
		lines[index] = strings.TrimSuffix(line, "\r")
	}
	return lines
}
