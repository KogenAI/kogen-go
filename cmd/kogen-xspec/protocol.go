package main

import (
	"context"
	"errors"
	"io"

	"kogen-go/internal/xspec/protocol"
)

// serveProtocol is the private JSON-lines adapter used by kogen-xspec. The
// transport owns framing and refusal; this file only composes production slice
// factories and selects the one named by argv.
func serveProtocol(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("kogen-xspec requires exactly one slice name")
	}
	if reason, unavailable := unavailableGSlices[args[0]]; unavailable {
		return errors.New(reason)
	}
	return protocol.Serve(ctx, args[0], xspecFactories(), input, output)
}
