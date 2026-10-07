package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"reflect"
	"unicode/utf8"

	"kogen-go/internal/contract"
	"kogen-go/internal/safefs"
)

// ToolOutputArguments selects a zero-based byte range from a prior full tool
// result. A nil OutputLimit means through the end of the result.
type ToolOutputArguments struct {
	Handle       string
	OutputOffset uint64
	OutputLimit  *uint64
}

// ParseToolOutputArguments validates the known fields and ignores extra keys,
// matching the provider tool argument contract.
func ParseToolOutputArguments(raw json.RawMessage) (ToolOutputArguments, error) {
	object, err := decodeToolObject(raw)
	if err != nil {
		return ToolOutputArguments{}, toolError(invalidArgumentsText)
	}
	var arguments ToolOutputArguments
	if value, ok := object["handle"]; !ok || json.Unmarshal(value, &arguments.Handle) != nil {
		return ToolOutputArguments{}, toolError(invalidArgumentsText)
	}
	if value, ok := object["output_offset"]; ok {
		arguments.OutputOffset, err = unsignedInteger(value)
		if err != nil {
			return ToolOutputArguments{}, toolError(invalidArgumentsText)
		}
	}
	if value, ok := object["output_limit"]; ok {
		limit, parseErr := unsignedInteger(value)
		if parseErr != nil {
			return ToolOutputArguments{}, toolError(invalidArgumentsText)
		}
		arguments.OutputLimit = &limit
	}
	return arguments, nil
}

// ReadToolOutput opens a stored redacted result only when its handle is a
// lowercase SHA-256 and its backing file is a private regular file whose bytes
// match that digest. Symlinks and stale or altered files are refused.
func ReadToolOutput(root contract.RootedFS, arguments ToolOutputArguments) (string, error) {
	if !validToolResultHandle(arguments.Handle) || root == nil {
		return "", toolError(unknownOutputHandle)
	}
	name := "logs/" + toolResultFilePrefix + arguments.Handle + toolResultFileSuffix
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return "", toolError(unknownOutputHandle)
	}
	contents, err := readPrivateRegularFile(root, name)
	if err != nil || !utf8.Valid(contents) {
		return "", toolError(unknownOutputHandle)
	}
	if !equalBytes(redactToolOutput(contents), contents) {
		return "", toolError(unknownOutputHandle)
	}
	digest := sha256.Sum256(contents)
	if hex.EncodeToString(digest[:]) != arguments.Handle {
		return "", toolError(unknownOutputHandle)
	}
	text := string(contents)
	start := minUint64(arguments.OutputOffset, uint64(len(text)))
	end := uint64(len(text))
	if arguments.OutputLimit != nil {
		limit := *arguments.OutputLimit
		if ^uint64(0)-arguments.OutputOffset < limit {
			end = uint64(len(text))
		} else {
			end = minUint64(arguments.OutputOffset+limit, uint64(len(text)))
		}
	}
	if end < start {
		end = start
	}
	byteStart := ceilUTF8Boundary(text, int(start))
	byteEnd := floorUTF8Boundary(text, int(end))
	if byteEnd < byteStart {
		return "", nil
	}
	return text[byteStart:byteEnd], nil
}

func readPrivateRegularFile(root contract.RootedFS, name string) ([]byte, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&fs.ModeSymlink != 0 || before.Mode().Perm()&0o077 != 0 {
		return nil, safefs.ErrUnsafeFile
	}
	file, err := root.OpenRead(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, hasStat := file.(interface{ Stat() (fs.FileInfo, error) })
	var openedInfo fs.FileInfo
	if hasStat {
		openedInfo, err = opened.Stat()
		if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&0o077 != 0 || !sameRootedFile(before, openedInfo) {
			return nil, errors.Join(err, safefs.ErrUnsafeFile)
		}
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	after, err := root.Lstat(name)
	if err != nil || !after.Mode().IsRegular() || after.Mode()&fs.ModeSymlink != 0 || after.Mode().Perm()&0o077 != 0 || !sameRootedFile(before, after) {
		return nil, errors.Join(err, safefs.ErrUnsafeFile)
	}
	if hasStat && !sameRootedFile(openedInfo, after) {
		return nil, safefs.ErrUnsafeFile
	}
	return contents, nil
}

func sameRootedFile(left, right fs.FileInfo) bool {
	leftStat := reflect.ValueOf(left.Sys())
	rightStat := reflect.ValueOf(right.Sys())
	if leftStat.Kind() == reflect.Pointer {
		leftStat = leftStat.Elem()
	}
	if rightStat.Kind() == reflect.Pointer {
		rightStat = rightStat.Elem()
	}
	if !leftStat.IsValid() || !rightStat.IsValid() || leftStat.Kind() != reflect.Struct || rightStat.Kind() != reflect.Struct {
		return false
	}
	leftDevice, rightDevice := leftStat.FieldByName("Dev"), rightStat.FieldByName("Dev")
	leftInode, rightInode := leftStat.FieldByName("Ino"), rightStat.FieldByName("Ino")
	return leftDevice.IsValid() && rightDevice.IsValid() && leftInode.IsValid() && rightInode.IsValid() &&
		reflect.DeepEqual(leftDevice.Interface(), rightDevice.Interface()) && reflect.DeepEqual(leftInode.Interface(), rightInode.Interface())
}

func validToolResultHandle(handle string) bool {
	if len(handle) != 64 {
		return false
	}
	for _, character := range handle {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func minUint64(left, right uint64) uint64 {
	if left < right {
		return left
	}
	return right
}
