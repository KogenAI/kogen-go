package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := serveProtocol(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "kogen-xspec: %v\n", err)
		os.Exit(2)
	}
}
