//go:build linux

package recovery

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func nativeProcessStartedMS(pid int64) (int64, bool, error) {
	if pid <= 0 {
		return 0, false, errors.New("process id must be positive")
	}
	contents, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	closeParen := strings.LastIndexByte(string(contents), ')')
	if closeParen < 0 || closeParen+1 >= len(contents) {
		return 0, false, errors.New("malformed /proc process stat")
	}
	fields := strings.Fields(string(contents[closeParen+1:]))
	if len(fields) <= 19 {
		return 0, false, errors.New("short /proc process stat")
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return 0, false, nil
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse /proc process start ticks: %w", err)
	}
	hz, err := linuxClockTicksPerSecond()
	if err != nil {
		return 0, false, err
	}
	bootSeconds, err := linuxBootTimeSeconds()
	if err != nil {
		return 0, false, err
	}
	startedMS := bootSeconds*1_000 + startTicks*1_000/hz
	return int64(startedMS), true, nil
}

func linuxClockTicksPerSecond() (uint64, error) {
	const atClockTicks = 17
	contents, err := os.ReadFile("/proc/self/auxv")
	if err != nil {
		return 0, fmt.Errorf("read process clock tick rate: %w", err)
	}
	wordSize := strconv.IntSize / 8
	for offset := 0; offset+2*wordSize <= len(contents); offset += 2 * wordSize {
		var kind, value uint64
		if wordSize == 8 {
			kind = binary.NativeEndian.Uint64(contents[offset : offset+wordSize])
			value = binary.NativeEndian.Uint64(contents[offset+wordSize : offset+2*wordSize])
		} else {
			kind = uint64(binary.NativeEndian.Uint32(contents[offset : offset+wordSize]))
			value = uint64(binary.NativeEndian.Uint32(contents[offset+wordSize : offset+2*wordSize]))
		}
		if kind == 0 {
			break
		}
		if kind == atClockTicks && value > 0 {
			return value, nil
		}
	}
	return 0, errors.New("process clock tick rate is absent from /proc/self/auxv")
}

func linuxBootTimeSeconds() (uint64, error) {
	contents, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, fmt.Errorf("read process boot time: %w", err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "btime" {
			seconds, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse process boot time: %w", err)
			}
			return seconds, nil
		}
	}
	return 0, errors.New("process boot time is absent from /proc/stat")
}
