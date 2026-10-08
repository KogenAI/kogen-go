// Package ladder runs recipe attempts sequentially from the immutable Build
// base, preserving each attempt and carrying only compact failure summaries
// into later builder requests.
package ladder
