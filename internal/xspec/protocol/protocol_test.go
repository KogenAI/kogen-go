package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type fixtureSlice struct {
	count          int
	resetCount     int
	applyCount     int
	observeBody    json.RawMessage
	useObserveBody bool
	closed         bool
}

func (s *fixtureSlice) Reset(context.Context) error {
	s.count = 0
	s.resetCount++
	return nil
}

func (s *fixtureSlice) HasEventTag(tag string) bool {
	return tag == "Known"
}

func (s *fixtureSlice) Apply(_ context.Context, event Event) error {
	if event.Tag != "Known" {
		return fmt.Errorf("unknown event tag %q", event.Tag)
	}
	if !event.HasValue || len(event.Value) == 0 || event.Value[0] != '{' {
		return errors.New("Known event requires an object value")
	}
	s.count++
	s.applyCount++
	return nil
}

func (s *fixtureSlice) Observe(context.Context) (json.RawMessage, error) {
	if s.useObserveBody {
		return append(json.RawMessage(nil), s.observeBody...), nil
	}
	return json.RawMessage(fmt.Sprintf(`{"count":%d,"allItems":["a","b"],"nested":{"complete":true}}`, s.count)), nil
}

func (s *fixtureSlice) Close() error {
	s.closed = true
	return nil
}

func fixtureRegistry(s *fixtureSlice) Registry {
	return Registry{"test": func(context.Context) (Slice, error) { return s, nil }}
}

func TestServeResetApplyEmitsAndFlushesFullObservations(t *testing.T) {
	slice := &fixtureSlice{}
	output := &bytes.Buffer{}
	input := &barrierReader{
		chunks: [][]byte{
			[]byte(`{"op":"reset"}` + "\r\n"),
			[]byte(`{"op":"apply","event":{"tag":"Known","value":{"n":1}}}`),
		},
		beforeSecond: func() error {
			if got := output.String(); got != "{\"count\":0,\"allItems\":[\"a\",\"b\"],\"nested\":{\"complete\":true}}\n" {
				return fmt.Errorf("first observation was not flushed before next read: %q", got)
			}
			return nil
		},
	}
	if err := Serve(context.Background(), "test", fixtureRegistry(slice), input, output); err != nil {
		t.Fatal(err)
	}
	want := "{\"count\":0,\"allItems\":[\"a\",\"b\"],\"nested\":{\"complete\":true}}\n" +
		"{\"count\":1,\"allItems\":[\"a\",\"b\"],\"nested\":{\"complete\":true}}\n"
	if got := output.String(); got != want {
		t.Fatalf("observations = %q, want %q", got, want)
	}
	if slice.resetCount != 1 || slice.applyCount != 1 {
		t.Fatalf("transition counts: reset=%d apply=%d", slice.resetCount, slice.applyCount)
	}
	if !slice.closed {
		t.Fatal("slice fixture was not closed on EOF")
	}
}

func TestServeResetRestoresSliceState(t *testing.T) {
	slice := &fixtureSlice{}
	input := strings.NewReader("{\"op\":\"apply\",\"event\":{\"tag\":\"Known\",\"value\":{}}}\n" +
		"{\"op\":\"reset\"}\n" +
		"{\"op\":\"apply\",\"event\":{\"tag\":\"Known\",\"value\":{}}}\n")
	var output bytes.Buffer
	if err := Serve(context.Background(), "test", fixtureRegistry(slice), input, &output); err != nil {
		t.Fatal(err)
	}
	want := "{\"count\":1,\"allItems\":[\"a\",\"b\"],\"nested\":{\"complete\":true}}\n" +
		"{\"count\":0,\"allItems\":[\"a\",\"b\"],\"nested\":{\"complete\":true}}\n" +
		"{\"count\":1,\"allItems\":[\"a\",\"b\"],\"nested\":{\"complete\":true}}\n"
	if got := output.String(); got != want {
		t.Fatalf("observations = %q, want %q", got, want)
	}
}

func TestUnwiredSliceAndProjectionRequestsRefuse(t *testing.T) {
	t.Run("unwired slice has no fallback seam", func(t *testing.T) {
		var output bytes.Buffer
		err := Serve(context.Background(), "unwired", nil, strings.NewReader("{\"op\":\"reset\"}\n"), &output)
		if !errors.Is(err, ErrUnknownSlice) {
			t.Fatalf("Serve error = %v, want ErrUnknownSlice", err)
		}
		if output.Len() != 0 {
			t.Fatalf("unwired slice emitted a placeholder observation: %q", output.String())
		}
	})

	t.Run("projection operation is refused", func(t *testing.T) {
		slice := &fixtureSlice{}
		var output bytes.Buffer
		err := Serve(context.Background(), "test", fixtureRegistry(slice), strings.NewReader("{\"op\":\"project\"}\n"), &output)
		if !errors.Is(err, ErrUnsupportedOperation) {
			t.Fatalf("Serve error = %v, want ErrUnsupportedOperation", err)
		}
		if output.Len() != 0 {
			t.Fatalf("projection request emitted an observation: %q", output.String())
		}
	})

	t.Run("projection field is refused", func(t *testing.T) {
		slice := &fixtureSlice{}
		var output bytes.Buffer
		request := `{"op":"apply","event":{"tag":"Known","value":{}},"project":["count"]}`
		err := Serve(context.Background(), "test", fixtureRegistry(slice), strings.NewReader(request+"\n"), &output)
		if err == nil || !strings.Contains(err.Error(), `unknown field "project"`) {
			t.Fatalf("Serve error = %v, want unknown projection field", err)
		}
		if output.Len() != 0 || slice.applyCount != 0 {
			t.Fatalf("projection request was applied or emitted output: output=%q apply=%d", output.String(), slice.applyCount)
		}
	})
}

func TestServeRefusesMalformedTypesAndUnknownTags(t *testing.T) {
	cases := []struct {
		name    string
		request string
		wantErr string
	}{
		{name: "root must be object", request: `[]`, wantErr: "expected one JSON object"},
		{name: "operation must be string", request: `{"op":1}`, wantErr: "non-empty string field `op`"},
		{name: "reset has no event", request: `{"op":"reset","event":{}}`, wantErr: "must not contain `event`"},
		{name: "apply requires event", request: `{"op":"apply"}`, wantErr: "requires object field `event`"},
		{name: "event must be object", request: `{"op":"apply","event":null}`, wantErr: "expected one JSON object"},
		{name: "tag required", request: `{"op":"apply","event":{}}`, wantErr: "requires non-empty string field `tag`"},
		{name: "tag must be string", request: `{"op":"apply","event":{"tag":7}}`, wantErr: "requires non-empty string field `tag`"},
		{name: "unknown tag", request: `{"op":"apply","event":{"tag":"Other","value":{}}}`, wantErr: "unknown event tag"},
		{name: "wrong value type", request: `{"op":"apply","event":{"tag":"Known","value":"text"}}`, wantErr: "requires an object value"},
		{name: "duplicate tag field", request: `{"op":"apply","event":{"tag":"Known","tag":"Known","value":{}}}`, wantErr: `duplicate field "tag"`},
		{name: "unknown request field", request: `{"op":"reset","extra":true}`, wantErr: `unknown field "extra"`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			slice := &fixtureSlice{}
			var output bytes.Buffer
			err := Serve(context.Background(), "test", fixtureRegistry(slice), strings.NewReader(test.request+"\n"), &output)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Serve error = %v, want substring %q", err, test.wantErr)
			}
			if output.Len() != 0 {
				t.Fatalf("refused request emitted an observation: %q", output.String())
			}
		})
	}
}

func TestServeRefusesInvalidUTF8AndOversizedLines(t *testing.T) {
	t.Run("invalid UTF-8", func(t *testing.T) {
		var output bytes.Buffer
		input := append([]byte(`{"op":"reset"}`), 0xff, '\n')
		err := Serve(context.Background(), "test", fixtureRegistry(&fixtureSlice{}), bytes.NewReader(input), &output)
		if err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
			t.Fatalf("Serve error = %v, want invalid UTF-8 refusal", err)
		}
		if output.Len() != 0 {
			t.Fatalf("invalid UTF-8 emitted an observation: %q", output.String())
		}
	})

	t.Run("line limit", func(t *testing.T) {
		var output bytes.Buffer
		input := strings.NewReader(strings.Repeat(" ", MaxLineBytes+1))
		err := Serve(context.Background(), "test", fixtureRegistry(&fixtureSlice{}), input, &output)
		if err == nil || !strings.Contains(err.Error(), "exceeds 16000000-byte limit") {
			t.Fatalf("Serve error = %v, want bounded-line refusal", err)
		}
		if output.Len() != 0 {
			t.Fatalf("oversized line emitted an observation: %q", output.String())
		}
	})
}

func TestServeRejectsNonObjectAndMalformedObservations(t *testing.T) {
	for _, observation := range []json.RawMessage{
		nil,
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`{"open":`),
		json.RawMessage(`{"count":1,"count":2}`),
		json.RawMessage{'{', '"', 0xff, '"', ':', '1', '}'},
		json.RawMessage(`{"last":"no_seam"}`),
		json.RawMessage(`{"no_seam":true}`),
	} {
		t.Run(fmt.Sprintf("observation-%q", observation), func(t *testing.T) {
			slice := &fixtureSlice{observeBody: observation, useObserveBody: true}
			var output bytes.Buffer
			err := Serve(context.Background(), "test", fixtureRegistry(slice), strings.NewReader("{\"op\":\"reset\"}\n"), &output)
			if err == nil || !strings.Contains(err.Error(), "invalid full observation") {
				t.Fatalf("Serve error = %v, want full-observation refusal (body=%q, len=%d)", err, observation, len(observation))
			}
			if output.Len() != 0 {
				t.Fatalf("invalid observation was emitted: %q", output.String())
			}
		})
	}
}

func TestServeAcceptsFinalUnterminatedJSONLRecord(t *testing.T) {
	var output bytes.Buffer
	err := Serve(context.Background(), "test", fixtureRegistry(&fixtureSlice{}), strings.NewReader(`{"op":"reset"}`), &output)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "{\"count\":0,\"allItems\":[\"a\",\"b\"],\"nested\":{\"complete\":true}}\n"; got != want {
		t.Fatalf("observation = %q, want %q", got, want)
	}
}

type barrierReader struct {
	chunks       [][]byte
	index        int
	beforeSecond func() error
}

func (r *barrierReader) Read(dst []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	if r.index == 1 && r.beforeSecond != nil {
		if err := r.beforeSecond(); err != nil {
			return 0, err
		}
	}
	chunk := r.chunks[r.index]
	r.index++
	if len(chunk) > len(dst) {
		copy(dst, chunk[:len(dst)])
		return len(dst), nil
	}
	return copy(dst, chunk), nil
}
