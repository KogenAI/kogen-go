package protection

import (
	"fmt"
	"strings"
)

// Glob is a validated spec path glob. It is intentionally independent of Git
// ignore rules: it matches canonical slash-separated repository paths and
// treats dotfiles like any other path component.
type Glob struct {
	pattern      string
	alternatives []string
	subtree      bool
}

// CompileGlob validates a §2.5.2 protected-path glob.
func CompileGlob(pattern string) (*Glob, error) {
	if pattern == "" || strings.ContainsAny(pattern, "\\\x00\r\n") || strings.HasPrefix(pattern, "/") {
		return nil, fmt.Errorf("invalid protected path glob %q", pattern)
	}
	subtree := strings.HasSuffix(pattern, "/")
	if subtree {
		pattern = strings.TrimSuffix(pattern, "/")
		if pattern == "" {
			return nil, fmt.Errorf("invalid protected path glob %q", pattern)
		}
	}
	for _, component := range strings.Split(pattern, "/") {
		if component == "" || component == "." || component == ".." || strings.EqualFold(component, ".git") {
			return nil, fmt.Errorf("invalid protected path glob %q", pattern)
		}
	}
	alternatives, err := expandBraces(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid protected path glob %q: %w", pattern, err)
	}
	for _, alternative := range alternatives {
		for _, component := range strings.Split(alternative, "/") {
			if component == "" || component == "." || component == ".." || strings.EqualFold(component, ".git") {
				return nil, fmt.Errorf("invalid protected path glob %q", pattern)
			}
		}
		if err := validateGlobSyntax(alternative); err != nil {
			return nil, fmt.Errorf("invalid protected path glob %q: %w", pattern, err)
		}
	}
	return &Glob{pattern: pattern, alternatives: alternatives, subtree: subtree}, nil
}

// Match reports whether the glob selects path. A trailing slash selects every
// file beneath that directory, at any depth, and not a same-named file.
func (g *Glob) Match(path string) bool {
	if g == nil || path == "" {
		return false
	}
	if g.subtree {
		for _, pattern := range g.alternatives {
			if globMatch(pattern+"/**", path) {
				return true
			}
		}
		return false
	}
	for _, pattern := range g.alternatives {
		if globMatch(pattern, path) {
			return true
		}
	}
	return false
}

func validateGlobSyntax(pattern string) error {
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '[':
			end := classEnd(pattern, index)
			if end < 0 {
				return fmt.Errorf("unterminated character class")
			}
			if end == index+1 || end == index+2 && (pattern[index+1] == '!' || pattern[index+1] == '^') {
				return fmt.Errorf("empty character class")
			}
			index = end
		case ']':
			return fmt.Errorf("unmatched ]")
		case '{', '}':
			return fmt.Errorf("unexpanded brace")
		}
	}
	return nil
}

func classEnd(pattern string, start int) int {
	index := start + 1
	if index < len(pattern) && (pattern[index] == '!' || pattern[index] == '^') {
		index++
	}
	if index < len(pattern) && pattern[index] == ']' {
		index++
	}
	for ; index < len(pattern); index++ {
		if pattern[index] == ']' {
			return index
		}
	}
	return -1
}

func expandBraces(pattern string) ([]string, error) {
	result := make([]string, 0, 1)
	var expand func(string) error
	expand = func(current string) error {
		start, end, parts, found, err := firstBrace(current)
		if err != nil {
			return err
		}
		if !found {
			result = append(result, current)
			if len(result) > 256 {
				return fmt.Errorf("too many brace alternatives")
			}
			return nil
		}
		for _, part := range parts {
			if err := expand(current[:start] + part + current[end+1:]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := expand(pattern); err != nil {
		return nil, err
	}
	return result, nil
}

func firstBrace(pattern string) (int, int, []string, bool, error) {
	start := strings.IndexByte(pattern, '{')
	if start < 0 {
		if strings.ContainsRune(pattern, '}') {
			return 0, 0, nil, false, fmt.Errorf("unmatched }")
		}
		return 0, 0, nil, false, nil
	}
	depth, end := 0, -1
	for index := start; index < len(pattern); index++ {
		switch pattern[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = index
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return 0, 0, nil, false, fmt.Errorf("unterminated brace alternative")
	}
	parts := make([]string, 0, 2)
	partStart, nested := start+1, 0
	for index := start + 1; index < end; index++ {
		switch pattern[index] {
		case '{':
			nested++
		case '}':
			nested--
		case ',':
			if nested == 0 {
				parts = append(parts, pattern[partStart:index])
				partStart = index + 1
			}
		}
	}
	parts = append(parts, pattern[partStart:end])
	if len(parts) == 1 {
		return 0, 0, nil, false, fmt.Errorf("brace alternatives require a comma")
	}
	return start, end, parts, true, nil
}

func globMatch(pattern, candidate string) bool {
	pat, value := []byte(pattern), []byte(candidate)
	type state struct{ pi, vi int }
	seen := make(map[state]bool)
	known := make(map[state]bool)
	var match func(int, int) bool
	match = func(pi, vi int) bool {
		key := state{pi: pi, vi: vi}
		if known[key] {
			return seen[key]
		}
		known[key] = true
		ok := false
		if pi == len(pat) {
			ok = vi == len(value)
		} else if pat[pi] == '*' {
			if pi+1 < len(pat) && pat[pi+1] == '*' {
				next := pi + 2
				if next < len(pat) && pat[next] == '/' {
					next++
					ok = match(next, vi)
					for cursor := vi; !ok && cursor < len(value); cursor++ {
						if value[cursor] == '/' {
							ok = match(next, cursor+1)
						}
					}
				} else {
					for cursor := vi; !ok && cursor <= len(value); cursor++ {
						ok = match(next, cursor)
					}
				}
			} else {
				ok = match(pi+1, vi)
				for cursor := vi; !ok && cursor < len(value) && value[cursor] != '/'; cursor++ {
					ok = match(pi+1, cursor+1)
				}
			}
		} else if pat[pi] == '?' {
			ok = vi < len(value) && value[vi] != '/' && match(pi+1, vi+1)
		} else if pat[pi] == '[' {
			end := classEnd(string(pat), pi)
			ok = vi < len(value) && value[vi] != '/' && classContains(pat[pi+1:end], value[vi]) && match(end+1, vi+1)
		} else {
			ok = vi < len(value) && pat[pi] == value[vi] && match(pi+1, vi+1)
		}
		seen[key] = ok
		return ok
	}
	return match(0, 0)
}

func classContains(class []byte, value byte) bool {
	negated := len(class) > 0 && (class[0] == '!' || class[0] == '^')
	if negated {
		class = class[1:]
	}
	included := false
	for index := 0; index < len(class); {
		if index+2 < len(class) && class[index+1] == '-' {
			if class[index] <= value && value <= class[index+2] {
				included = true
			}
			index += 3
			continue
		}
		if class[index] == value {
			included = true
		}
		index++
	}
	if negated {
		return !included
	}
	return included
}

func hasGlobMagic(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[{")
}
