package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"kogen-go/internal/xspec/intentapprove"
	"kogen-go/internal/xspec/protocol"
	"kogen-go/internal/xspec/queuestatus"
)

// serveProtocol is the private JSON-lines adapter used by kogen-xspec. The
// transport owns framing and refusal; this file only composes production slice
// factories and selects the one named by argv.
func serveProtocol(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("kogen-xspec requires exactly one slice name")
	}
	return protocol.Serve(ctx, args[0], xspecFactories(), input, output)
}

func xspecFactories() protocol.Registry {
	registry := make(protocol.Registry)
	for _, source := range []protocol.Registry{intentapprove.Factories(), queuestatus.Factories()} {
		for name, factory := range source {
			if _, exists := registry[name]; exists {
				panic(fmt.Sprintf("duplicate xspec factory %q", name))
			}
			registry[name] = factory
		}
	}
	return registry
}
