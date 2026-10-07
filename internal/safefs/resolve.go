package safefs

import (
	"io/fs"
	"strings"
)

// validateName accepts the canonical slash-separated names used by io/fs.
// The root itself is represented only by "." and only where allowRoot is true.
func validateName(name string, allowRoot bool) ([]string, error) {
	if strings.ContainsRune(name, 0) {
		return nil, ErrUnsafePath
	}
	if name == "." && allowRoot {
		return nil, nil
	}
	if !fs.ValidPath(name) || name == "." {
		return nil, ErrUnsafePath
	}
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if forbiddenComponent(part) {
			return nil, ErrUnsafePath
		}
	}
	return parts, nil
}

// splitLinkTarget parses a symlink target without normalizing away its parent
// components. The descriptor walker applies those components against its
// already-open directory stack and refuses any attempt to pop above the root.
func splitLinkTarget(target string) ([]string, error) {
	if target == "" || strings.ContainsRune(target, 0) || strings.HasPrefix(target, "/") {
		return nil, ErrUnsafePath
	}
	parts := strings.Split(target, "/")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part != ".." && forbiddenComponent(part) {
			return nil, ErrUnsafePath
		}
		result = append(result, part)
	}
	return result, nil
}

func forbiddenComponent(part string) bool {
	if strings.EqualFold(part, ".git") {
		return true
	}
	// Keep canonical rooted names portable to Windows too. The first-dot rule
	// covers device aliases such as NUL.txt, not just the bare reserved name.
	base, _, _ := strings.Cut(part, ".")
	switch strings.ToUpper(base) {
	case "NUL", "CON", "PRN", "AUX",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}

// validateSymlinkTarget checks a relative target against the logical directory
// that contains the link. The returned stack is useful to callers that need to
// reason about the target without opening it.
func validateSymlinkTarget(parent []string, target string) ([]string, error) {
	parts, err := splitLinkTarget(target)
	if err != nil {
		return nil, err
	}
	stack := append([]string(nil), parent...)
	for _, part := range parts {
		if part == ".." {
			if len(stack) == 0 {
				return nil, ErrUnsafePath
			}
			stack = stack[:len(stack)-1]
			continue
		}
		stack = append(stack, part)
	}
	return stack, nil
}
