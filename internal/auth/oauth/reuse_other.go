//go:build !darwin && !linux

package oauth

import "syscall"

func setReuseAddress(_ string, _ string, _ syscall.RawConn) error { return nil }
