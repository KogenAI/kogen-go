//go:build linux

package single

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func currentProcessStartedMS() (int64, error) {
	contents, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", os.Getpid()))
	if err != nil {
		return 0, fmt.Errorf("read current process identity: %w", err)
	}
	closeParen := strings.LastIndexByte(string(contents), ')')
	if closeParen < 0 || closeParen+1 >= len(contents) {
		return 0, errors.New("malformed /proc process stat")
	}
	fields := strings.Fields(string(contents[closeParen+1:]))
	if len(fields) <= 19 || fields[0] == "Z" || fields[0] == "X" {
		return 0, errors.New("current process start identity is unavailable")
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse current process start ticks: %w", err)
	}
	auxv, err := os.ReadFile("/proc/self/auxv")
	if err != nil {
		return 0, fmt.Errorf("read current process clock tick rate: %w", err)
	}
	wordSize := strconv.IntSize / 8
	var hz uint64
	for offset := 0; offset+2*wordSize <= len(auxv); offset += 2 * wordSize {
		var kind, value uint64
		if wordSize == 8 {
			kind = binary.NativeEndian.Uint64(auxv[offset : offset+wordSize])
			value = binary.NativeEndian.Uint64(auxv[offset+wordSize : offset+2*wordSize])
		} else {
			kind = uint64(binary.NativeEndian.Uint32(auxv[offset : offset+wordSize]))
			value = uint64(binary.NativeEndian.Uint32(auxv[offset+wordSize : offset+2*wordSize]))
		}
		if kind == 0 {
			break
		}
		if kind == 17 && value > 0 {
			hz = value
			break
		}
	}
	if hz == 0 {
		return 0, errors.New("process clock tick rate is absent from /proc/self/auxv")
	}
	procStat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, fmt.Errorf("read system boot time: %w", err)
	}
	var boot uint64
	for _, line := range strings.Split(string(procStat), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "btime" {
			boot, err = strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse system boot time: %w", err)
			}
			break
		}
	}
	if boot == 0 {
		return 0, errors.New("system boot time is absent from /proc/stat")
	}
	return int64(boot*1_000 + startTicks*1_000/hz), nil
}
