//go:build tools

// Package tools pins the syscall dependency before production users are added.
package tools

import _ "golang.org/x/sys/unix"
