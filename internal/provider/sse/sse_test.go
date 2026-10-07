package sse

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestByteChunksNormalizeLineEndingsJoinDataAndFlushTail(t *testing.T) {
	wire := strings.Join([]string{
		": keepalive\r\n\r\n",
		"event: response.output_item.done\r",
		`data: {"type":"response.output_item.done",` + "\r",
		`data: "item":{"type":"function_call","id":"fc1","status":"completed","call_id":"call1","name":"shell","arguments":{"cmd":"printf ok"}}}` + "\r\n\r\n",
		"data: [DONE]\n\n",
		"data:{\"type\":\"response.completed\",\r",
		`data: "response":{"id":"resp1","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5,"input_tokens_details":{"cached_tokens":3}}}}`,
	}, "")

	assembler := NewAssembler()
	for i := range []byte(wire) {
		if err := assembler.Feed([]byte{wire[i]}); err != nil {
			t.Fatalf("feed byte %d: %v", i, err)
		}
	}
	if !assembler.HasItems() {
		t.Fatal("expected completed item to be visible for continuation decisions")
	}
	response, err := assembler.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if response.ID != "resp1" || len(response.RawItems) != 1 || len(response.ToolCalls) != 1 {
		t.Fatalf("unexpected assembled response: %#v", response)
	}
	if response.ToolCalls[0].Arguments == nil || string(response.ToolCalls[0].Arguments) != `{"cmd":"printf ok"}` {
		t.Fatalf("arguments were not assembled as an object: %s", response.ToolCalls[0].Arguments)
	}
	if got := *response.Usage.Input; got != 7 {
		t.Fatalf("uncached input = %d, want 7", got)
	}
	if got := *response.Usage.CachedInput; got != 3 {
		t.Fatalf("cached input = %d, want 3", got)
	}
	if err := assembler.Feed([]byte("ignored after finish")); err != nil {
		t.Fatalf("feed after finish: %v", err)
	}
}

func TestCompletedOutputOverridesCollectedItemsAndAssemblesFunctionArgumentsSafely(t *testing.T) {
	assembler := NewAssembler()
	feedFrame(t, assembler, `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"stale"}]}}`)
	feedFrame(t, assembler, `{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"final "},{"type":"output_text","text":"text"}]},{"type":"function_call","status":"completed","call_id":"object","name":"read","arguments":{"path":"one"}},{"type":"function_call","status":"completed","call_id":"string","name":"read","arguments":"{\"path\":\"two\"}"},{"type":"function_call","status":"in_progress"}],"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":35},"cache_write_tokens":4,"output_tokens_details":{"reasoning_tokens":8}}}}`)

	response, err := assembler.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if response.Text != "final text" || len(response.RawItems) != 4 || len(response.ToolCalls) != 2 {
		t.Fatalf("completed output did not take precedence: %#v", response)
	}
	if string(response.ToolCalls[0].Arguments) != `{"path":"one"}` || string(response.ToolCalls[1].Arguments) != `{"path":"two"}` {
		t.Fatalf("function arguments not preserved as objects: %#v", response.ToolCalls)
	}
	if *response.Usage.Input != 65 || *response.Usage.CachedInput != 35 || *response.Usage.CacheWrite != 4 || *response.Usage.Output != 20 || *response.Usage.Reasoning != 8 {
		t.Fatalf("usage fields were not mapped correctly: %#v", response.Usage)
	}
	if strings.Contains(string(response.RawItems[0]), "stale") {
		t.Fatalf("collected item used despite nonempty completed.output: %s", response.RawItems[0])
	}
}

func TestEmptyCompletedOutputFallsBackToCollectedItemsAndReturnsCopies(t *testing.T) {
	assembler := NewAssembler()
	feedFrame(t, assembler, `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"arrived"}]}}`)
	items := assembler.CollectedItems()
	items[0][0] = 'x'
	feedFrame(t, assembler, `{"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}`)
	response, err := assembler.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if response.Text != "arrived" || len(response.RawItems) != 1 {
		t.Fatalf("collected fallback was not used: %#v", response)
	}
}

func TestFailureWinsOverEarlierMalformedAndLaterMalformedEvents(t *testing.T) {
	assembler := NewAssembler()
	feedFrame(t, assembler, "not-json")
	feedFrame(t, assembler, `{"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}`)
	feedFrame(t, assembler, `{"type":"response.completed","response":{"id":"other","status":"completed","output":[]}}`)
	feedFrame(t, assembler, `{"type":"error","error":{"message":"rate_limit, then overload"}}`)
	_, err := assembler.Finish()
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderError, got %T: %v", err, err)
	}
	if providerErr.Kind != ErrorUsageLimit || providerErr.Message != "rate_limit, then overload" {
		t.Fatalf("failure precedence/classification = %#v", providerErr)
	}
}

func TestProviderFailureClassificationAndIncompleteUsage(t *testing.T) {
	tests := []struct {
		name string
		wire string
		kind ErrorKind
	}{
		{"rate limit", `{"type":"response.failed","error":{"message":"RATE LIMIT"}}`, ErrorUsageLimit},
		{"overload", `{"type":"response.failed","error":{"message":"overload"}}`, ErrorOverload},
		{"transport", `{"type":"response.failed","error":{"message":"socket reset"}}`, ErrorTransport},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assembler := NewAssembler()
			feedFrame(t, assembler, test.wire)
			_, err := assembler.Finish()
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || providerErr.Kind != test.kind {
				t.Fatalf("error = %#v, want kind %q", err, test.kind)
			}
		})
	}

	assembler := NewAssembler()
	feedFrame(t, assembler, `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":8,"output_tokens":2,"input_tokens_details":{"cached_tokens":3}}}}`)
	_, err := assembler.Finish()
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorIncomplete || providerErr.Usage == nil {
		t.Fatalf("incomplete event = %#v", err)
	}
	if providerErr.Message != "Model response incomplete (max_output_tokens); no tool calls were executed." || providerErr.Usage.Input == nil || *providerErr.Usage.Input != 5 {
		t.Fatalf("incomplete details/usage were lost: %#v", providerErr)
	}
}

func TestMalformedAndUnsafeFunctionArgumentsAreRefused(t *testing.T) {
	badArguments := []string{
		`[]`,
		`"[]"`,
		`"{\"cmd\": oops}"`,
		`{"cmd":1,"cmd":2}`,
		`null`,
	}
	for i, arguments := range badArguments {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			assembler := NewAssembler()
			feedFrame(t, assembler, `{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"function_call","status":"completed","call_id":"c","name":"shell","arguments":`+arguments+`}]}}`)
			_, err := assembler.Finish()
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || providerErr.Kind != ErrorMalformed {
				t.Fatalf("expected malformed function arguments, got %T %v", err, err)
			}
		})
	}
}

func TestCachedUsageAboveInputIsMalformed(t *testing.T) {
	assembler := NewAssembler()
	feedFrame(t, assembler, `{"type":"response.completed","response":{"id":"r","status":"completed","output":[],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":11}}}}`)
	_, err := assembler.Finish()
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorMalformed || providerErr.Message != "ChatGPT returned invalid token usage." {
		t.Fatalf("invalid cached usage result = %#v", err)
	}
}

func TestMissingUsageCountsRemainUnknownAndPreserveKnownZero(t *testing.T) {
	assembler := NewAssembler()
	feedFrame(t, assembler, `{"type":"response.completed","response":{"id":"r","status":"completed","output":[],"usage":{"input_tokens":null,"output_tokens":0,"input_tokens_details":{"cached_tokens":null}}}}`)
	response, err := assembler.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if response.Usage.Input != nil || response.Usage.CachedInput != nil || response.Usage.Output == nil || *response.Usage.Output != 0 {
		t.Fatalf("nullable token counts not preserved: %#v", response.Usage)
	}
}

func TestDuplicateCompletedMalformedAndMissingCompleted(t *testing.T) {
	t.Run("duplicate completed", func(t *testing.T) {
		assembler := NewAssembler()
		frame := `{"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}`
		feedFrame(t, assembler, frame)
		feedFrame(t, assembler, frame)
		_, err := assembler.Finish()
		assertMalformed(t, err)
	})
	t.Run("missing completed", func(t *testing.T) {
		assembler := NewAssembler()
		feedFrame(t, assembler, `[DONE]`)
		_, err := assembler.Finish()
		var providerErr *ProviderError
		if !errors.As(err, &providerErr) || providerErr.Kind != ErrorMalformed || providerErr.Message != "ChatGPT response stream did not complete." {
			t.Fatalf("missing completion = %#v", err)
		}
	})
	t.Run("invalid UTF-8", func(t *testing.T) {
		assembler := NewAssembler()
		frame := append([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\""), 0xff)
		frame = append(frame, []byte("\",\"status\":\"completed\"}}\n\n")...)
		if err := assembler.Feed(frame); err != nil {
			t.Fatal(err)
		}
		_, err := assembler.Finish()
		assertMalformed(t, err)
	})
}

func TestBodySizeLimitAcceptsExactlySixteenMegabytesAndRejectsMore(t *testing.T) {
	prefix := []byte(`data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}` + "\n\n")
	body := append(append([]byte(nil), prefix...), ':')
	body = append(body, bytes.Repeat([]byte{'x'}, MaxResponseBytes-len(body))...)
	if len(body) != MaxResponseBytes {
		t.Fatalf("fixture length = %d, want %d", len(body), MaxResponseBytes)
	}
	assembler := NewAssembler()
	if err := assembler.Feed(body); err != nil {
		t.Fatalf("exact-limit body rejected: %v", err)
	}
	if _, err := assembler.Finish(); err != nil {
		t.Fatalf("exact-limit response failed: %v", err)
	}

	assembler = NewAssembler()
	if err := assembler.Feed(body); err != nil {
		t.Fatalf("exact-limit prefix rejected: %v", err)
	}
	if err := assembler.Feed([]byte("x")); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("over-limit feed error = %v", err)
	}
	_, err := assembler.Finish()
	assertMalformed(t, err)
}

func TestFailureObservedBeforeLimitStillWinsOverSizeMalformed(t *testing.T) {
	prefix := []byte("data: {\"type\":\"error\",\"error\":{\"message\":\"rate_limit\"}}\n\n")
	body := append(append([]byte(nil), prefix...), ':')
	body = append(body, bytes.Repeat([]byte{'x'}, MaxResponseBytes-len(body))...)
	assembler := NewAssembler()
	if err := assembler.Feed(body); err != nil {
		t.Fatalf("exact-limit body rejected: %v", err)
	}
	if err := assembler.Feed([]byte("x")); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("over-limit feed error = %v", err)
	}
	_, err := assembler.Finish()
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorUsageLimit {
		t.Fatalf("failure did not precede size malformed: %#v", err)
	}
}

func TestEmptyFeedAtExactLimitDoesNotMakeBodyOversized(t *testing.T) {
	prefix := []byte(`data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}` + "\n\n")
	body := append(append([]byte(nil), prefix...), ':')
	body = append(body, bytes.Repeat([]byte{'x'}, MaxResponseBytes-len(body))...)
	assembler := NewAssembler()
	if err := assembler.Feed(body); err != nil {
		t.Fatal(err)
	}
	if err := assembler.Feed(nil); err != nil {
		t.Fatalf("empty feed at exact limit: %v", err)
	}
	if _, err := assembler.Finish(); err != nil {
		t.Fatalf("exact body after empty feed: %v", err)
	}
}

func feedFrame(t *testing.T, assembler *Assembler, data string) {
	t.Helper()
	if err := assembler.Feed([]byte("data: " + data + "\n\n")); err != nil {
		t.Fatalf("feed frame: %v", err)
	}
}

func assertMalformed(t *testing.T, err error) {
	t.Helper()
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorMalformed {
		t.Fatalf("expected malformed provider error, got %T %v", err, err)
	}
}
