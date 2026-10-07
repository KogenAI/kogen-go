package app

import (
	"fmt"
	"io"
)

// Bootstrap explicitly refuses commands until the production routes are wired.
func Bootstrap(command string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "%s: implementation bootstrap; command routes are not wired\n", command)
	return 2
}
