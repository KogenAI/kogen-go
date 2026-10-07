package intentapprove

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"kogen-go/internal/contract"
	"kogen-go/internal/xspec/protocol"
)

// Factories returns the production-backed factories for the two replay slice
// names. Integration may merge these with other private xspec factories.
func Factories() protocol.Registry {
	return protocol.Registry{"intent": IntentFactory, "approve": ApproveFactory}
}

// IntentFactory creates a temporary effect environment for the Intent
// lifecycle slice. The checkout begins without Intent source files.
func IntentFactory(ctx context.Context) (protocol.Slice, error) {
	w, err := newWorld(ctx, false)
	if err != nil {
		return nil, err
	}
	return &intentSlice{
		world: w, digestSymbols: map[string]string{}, last: "ok",
	}, nil
}

// ApproveFactory creates a temporary effect environment with two valid source
// pairs committed at the resolved base, matching the approve slice fixture.
func ApproveFactory(ctx context.Context) (protocol.Slice, error) {
	w, err := newWorld(ctx, true)
	if err != nil {
		return nil, err
	}
	return &approveSlice{
		world: w, last: "ok", feasibility: "", digestSymbols: map[string]string{},
		commitSymbols: map[contract.ObjectID]string{}, baseSymbols: map[contract.ObjectID]string{},
	}, nil
}

type eventValue map[string]json.RawMessage

func decodeValue(event protocol.Event) (eventValue, error) {
	if !event.HasValue || len(event.Value) == 0 || !json.Valid(event.Value) {
		return nil, errors.New("intentapprove: event requires one JSON value")
	}
	var value eventValue
	if err := json.Unmarshal(event.Value, &value); err != nil || value == nil {
		return nil, errors.New("intentapprove: event value must be an object")
	}
	return value, nil
}

func (v eventValue) text(name string) (string, error) {
	raw, ok := v[name]
	if !ok {
		return "", fmt.Errorf("intentapprove: missing string field %q", name)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", fmt.Errorf("intentapprove: field %q must be a string", name)
	}
	return text, nil
}

func (v eventValue) optionalText(name, fallback string) (string, error) {
	raw, ok := v[name]
	if !ok {
		return fallback, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", fmt.Errorf("intentapprove: field %q must be a string", name)
	}
	return text, nil
}

func (v eventValue) boolean(name string, fallback bool) (bool, error) {
	raw, ok := v[name]
	if !ok {
		return fallback, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("intentapprove: field %q must be a boolean", name)
	}
	return value, nil
}

func marshalObservation(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("intentapprove: encode observation: %w", err)
	}
	return json.RawMessage(data), nil
}

func resetWorld(ctx context.Context, current **world, seedSources bool) error {
	if *current != nil {
		if err := (*current).close(); err != nil {
			return err
		}
	}
	next, err := newWorld(ctx, seedSources)
	if err != nil {
		return err
	}
	*current = next
	return nil
}
