package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"unicode"
	"unicode/utf8"

	"kogen-go/internal/contract"
)

const (
	DefaultToolResultTokens uint64 = 2000
	minimumToolResultTokens uint64 = 128
	maximumToolResultTokens uint64 = 100000
	toolResultBytesPerToken uint64 = 4
	truncationNoticeReserve        = 320
	toolResultFilePrefix           = "tool-result-"
	toolResultFileSuffix           = ".log"
)

// BoundToolResult redacts credentials, converts non-UTF-8 payloads to a
// complete base64 representation, and applies the configured byte budget.
// When truncation is needed, it stores the complete redacted text beneath the
// rooted run logs directory and returns a content-addressed retrieval notice.
func BoundToolResult(runRoot contract.RootedFS, payload []byte, tokens uint64) (string, error) {
	if tokens < minimumToolResultTokens || tokens > maximumToolResultTokens {
		return "", toolError(invalidArgumentsText)
	}
	redacted := redactToolOutput(payload)
	if !utf8.Valid(redacted) {
		encoded := make([]byte, 0, len("[non-UTF-8 output, base64 encoded]\n")+base64.StdEncoding.EncodedLen(len(redacted)))
		encoded = append(encoded, "[non-UTF-8 output, base64 encoded]\n"...)
		buffer := make([]byte, base64.StdEncoding.EncodedLen(len(redacted)))
		base64.StdEncoding.Encode(buffer, redacted)
		redacted = append(encoded, buffer...)
	}
	capBytes := int(tokens * toolResultBytesPerToken)
	if len(redacted) <= capBytes {
		return string(redacted), nil
	}
	if runRoot == nil {
		return "", toolError("tool output store is unavailable")
	}
	if err := ensurePrivateToolDirectory(runRoot, "logs"); err != nil {
		return "", wrappedToolError("tool output store is unavailable", err)
	}
	handle := fmt.Sprintf("%x", sha256.Sum256(redacted))
	if err := storeToolResult(runRoot, handle, redacted); err != nil {
		return "", wrappedToolError("tool output store is unavailable", err)
	}
	return truncateToolResult(string(redacted), capBytes, handle), nil
}

func storeToolResult(root contract.RootedFS, handle string, contents []byte) error {
	name := "logs/" + toolResultFilePrefix + handle + toolResultFileSuffix
	if err := root.Publish(name, contents, 0o600, contract.PublicationCreateOnly); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrExist) {
		// The same content-addressed result can already exist from an earlier
		// tool call in this run. Validate it before treating it as a hit.
		info, statErr := root.Lstat(name)
		if statErr != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return err
		}
		previous, readErr := readPrivateRegularFile(root, name)
		if readErr != nil || !equalBytes(previous, contents) {
			return err
		}
		return nil
	}
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.Join(err, errors.New("existing tool result is not a private regular file"))
	}
	previous, err := readPrivateRegularFile(root, name)
	if err != nil || !equalBytes(previous, contents) {
		return errors.Join(err, errors.New("existing tool result does not match its handle"))
	}
	return nil
}

func truncateToolResult(text string, capBytes int, handle string) string {
	prefixLen := max(0, (capBytes-truncationNoticeReserve)/2)
	suffixLen := max(0, capBytes-truncationNoticeReserve-prefixLen)
	for range 32 {
		prefixLen = floorUTF8Boundary(text, min(prefixLen, len(text)))
		suffixLen = suffixFromEndUTF8Boundary(text, min(suffixLen, len(text)))
		shown := fmt.Sprintf("0..%d, %d..%d", prefixLen, len(text)-suffixLen, len(text))
		notice := truncationNotice(len(text), shown, handle)
		available := max(0, capBytes-len(notice))
		nextPrefix := floorUTF8Boundary(text, available/2)
		nextSuffix := suffixFromEndUTF8Boundary(text, available-available/2)
		result := text[:prefixLen] + notice + text[len(text)-suffixLen:]
		if len(result) <= capBytes && prefixLen == nextPrefix && suffixLen == nextSuffix {
			return result
		}
		prefixLen, suffixLen = nextPrefix, nextSuffix
	}
	// The minimum configured cap is much larger than the notice. This fallback
	// is defensive in case a future notice format grows unexpectedly.
	for prefixLen+suffixLen > capBytes/2 {
		if prefixLen >= suffixLen && prefixLen > 0 {
			prefixLen = floorUTF8Boundary(text, prefixLen-1)
		} else if suffixLen > 0 {
			suffixLen = suffixFromEndUTF8Boundary(text, suffixLen-1)
		} else {
			break
		}
	}
	shown := fmt.Sprintf("0..%d, %d..%d", prefixLen, len(text)-suffixLen, len(text))
	notice := truncationNotice(len(text), shown, handle)
	available := max(0, capBytes-len(notice))
	prefixLen = floorUTF8Boundary(text, min(prefixLen, available/2))
	suffixLen = suffixFromEndUTF8Boundary(text, min(suffixLen, available-available/2))
	shown = fmt.Sprintf("0..%d, %d..%d", prefixLen, len(text)-suffixLen, len(text))
	notice = truncationNotice(len(text), shown, handle)
	return text[:prefixLen] + notice + text[len(text)-suffixLen:]
}

func truncationNotice(total int, shown, handle string) string {
	return fmt.Sprintf("\n[truncated/range: %d bytes; shown byte ranges %s; retrieve with tool_output handle=%s, output_offset and output_limit]\n", total, shown, handle)
}

func redactToolOutput(payload []byte) []byte {
	output := make([]byte, 0, len(payload))
	for offset := 0; offset < len(payload); {
		spaceEnd := offset
		for spaceEnd < len(payload) {
			runeValue, size := utf8.DecodeRune(payload[spaceEnd:])
			if !unicode.IsSpace(runeValue) {
				break
			}
			spaceEnd += size
		}
		if spaceEnd > offset {
			output = append(output, payload[offset:spaceEnd]...)
			offset = spaceEnd
			if offset == len(payload) {
				break
			}
		}
		end := offset
		for end < len(payload) {
			runeValue, size := utf8.DecodeRune(payload[end:])
			if unicode.IsSpace(runeValue) {
				break
			}
			end += size
		}
		token := payload[offset:end]
		if bytes.EqualFold(token, []byte("bearer")) {
			output = append(output, token...)
			offset = end
			spaceEnd = offset
			for spaceEnd < len(payload) {
				runeValue, size := utf8.DecodeRune(payload[spaceEnd:])
				if !unicode.IsSpace(runeValue) {
					break
				}
				spaceEnd += size
			}
			output = append(output, payload[offset:spaceEnd]...)
			offset = spaceEnd
			if offset < len(payload) {
				secretEnd := offset
				for secretEnd < len(payload) {
					runeValue, size := utf8.DecodeRune(payload[secretEnd:])
					if unicode.IsSpace(runeValue) {
						break
					}
					secretEnd += size
				}
				output = append(output, "[REDACTED]"...)
				offset = secretEnd
			}
			continue
		}
		if looksLikeJWTBytes(token) || bytes.HasPrefix(token, []byte("sk-")) {
			output = append(output, "[REDACTED]"...)
		} else {
			output = append(output, token...)
		}
		offset = end
	}
	return output
}

func looksLikeJWTBytes(token []byte) bool {
	first := bytes.IndexByte(token, '.')
	if first <= 0 {
		return false
	}
	secondRelative := bytes.IndexByte(token[first+1:], '.')
	if secondRelative <= 0 {
		return false
	}
	second := first + 1 + secondRelative
	return second < len(token)-1 && bytes.IndexByte(token[second+1:], '.') < 0
}

func floorUTF8Boundary(text string, offset int) int {
	offset = min(max(offset, 0), len(text))
	for offset < len(text) && offset > 0 && !utf8.RuneStart(text[offset]) {
		offset--
	}
	return offset
}

func ceilUTF8Boundary(text string, offset int) int {
	offset = min(max(offset, 0), len(text))
	for offset < len(text) && !utf8.RuneStart(text[offset]) {
		offset++
	}
	return offset
}

func suffixFromEndUTF8Boundary(text string, length int) int {
	start := len(text) - min(max(length, 0), len(text))
	for start < len(text) && start > 0 && !utf8.RuneStart(text[start]) {
		start++
	}
	return len(text) - start
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
