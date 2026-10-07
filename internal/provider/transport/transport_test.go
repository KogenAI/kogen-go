package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testTransport(t *testing.T, deadlines Deadlines) *Client {
	t.Helper()
	client, err := New(nil, deadlines)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func testRequest(endpoint string) Request {
	return Request{
		Provider: ProviderChatGPT,
		Prepare: func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("request"))
		},
	}
}

func TestFirstBodyByteDeadlineIncludesRequestPreparation(t *testing.T) {
	deadlines := Deadlines{FirstByte: 30 * time.Millisecond, Idle: 80 * time.Millisecond, Total: 200 * time.Millisecond, MaxBodyBytes: 1024}
	client := testTransport(t, deadlines)
	started := time.Now()
	_, err := client.Do(context.Background(), Request{
		Provider: ProviderChatGPT,
		Prepare: func(ctx context.Context) (*http.Request, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("expected classified timeout, got %T %v", err, err)
	}
	if failure.Kind != KindTimeout {
		t.Fatalf("preparation deadline classified as %q", failure.Kind)
	}
	if elapsed := time.Since(started); elapsed < deadlines.FirstByte {
		t.Fatalf("attempt clock did not include preparation: %s", elapsed)
	}
}

func TestFirstByteDeadlineStartsAtBodyNotHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(120 * time.Millisecond)
		_, _ = io.WriteString(w, "first body byte")
	}))
	defer server.Close()

	client := testTransport(t, Deadlines{FirstByte: 35 * time.Millisecond, Idle: 90 * time.Millisecond, Total: 300 * time.Millisecond, MaxBodyBytes: 1024})
	_, err := client.Do(context.Background(), testRequest(server.URL))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindTimeout {
		t.Fatalf("headers without body bytes should time out, got %T %v", err, err)
	}
	if !failure.FirstBodyByteAt.IsZero() {
		t.Fatalf("first-body timestamp was set without a body byte: %v", failure.FirstBodyByteAt)
	}
}

func TestIdleDeadlineCountsKeepaliveBytesAndReturnsPartialBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": keepalive\n")
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, "data: later\n\n")
	}))
	defer server.Close()

	client := testTransport(t, Deadlines{FirstByte: 120 * time.Millisecond, Idle: 35 * time.Millisecond, Total: 300 * time.Millisecond, MaxBodyBytes: 1024})
	_, err := client.Do(context.Background(), testRequest(server.URL))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindStall {
		t.Fatalf("keepalive should start the idle clock, got %T %v", err, err)
	}
	if string(failure.PartialBody) != ": keepalive\n" || failure.FirstBodyByteAt.IsZero() {
		t.Fatalf("partial bytes or first-body time lost: %#v", failure)
	}
}

func TestInterruptedSSEBodyExposesOnlyCompletedRawItems(t *testing.T) {
	frame := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"partial\"}]}}\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, frame)
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
	}))
	defer server.Close()

	client := testTransport(t, Deadlines{FirstByte: 120 * time.Millisecond, Idle: 35 * time.Millisecond, Total: 300 * time.Millisecond, MaxBodyBytes: 1024})
	_, err := client.Do(context.Background(), testRequest(server.URL))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindStall {
		t.Fatalf("expected interrupted SSE stream, got %T %v", err, err)
	}
	items := failure.RetryItems()
	if len(items) != 1 || !strings.Contains(string(items[0]), "\"text\":\"partial\"") {
		t.Fatalf("completed partial items were not retained: %q", items)
	}
}

func TestTotalDeadlineWinsWhileBodyKeepsMakingProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher := w.(http.Flusher)
		for i := 0; i < 8; i++ {
			_, _ = io.WriteString(w, "x")
			flusher.Flush()
			time.Sleep(25 * time.Millisecond)
		}
	}))
	defer server.Close()

	client := testTransport(t, Deadlines{FirstByte: 250 * time.Millisecond, Idle: 150 * time.Millisecond, Total: 110 * time.Millisecond, MaxBodyBytes: 1024})
	_, err := client.Do(context.Background(), testRequest(server.URL))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindTimeout {
		t.Fatalf("active stream should stop at total cap, got %T %v", err, err)
	}
	if failure.BodyBytes == 0 {
		t.Fatal("expected body progress before total cap")
	}
}

func TestCallerCancellationIsNotAProviderRetryClass(t *testing.T) {
	firstByte := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "x")
		w.(http.Flusher).Flush()
		close(firstByte)
		<-r.Context().Done()
	}))
	defer server.Close()

	client := testTransport(t, Deadlines{FirstByte: time.Second, Idle: time.Second, Total: 2 * time.Second, MaxBodyBytes: 1024})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.Do(ctx, testRequest(server.URL))
		done <- err
	}()
	select {
	case <-firstByte:
	case <-time.After(time.Second):
		t.Fatal("server did not send first byte")
	}
	cancel()
	err := <-done
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindCancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation should remain distinct, got %T %v", err, err)
	}
}

func TestHTTPClassRetryAfterAndDiagnosticsAreSafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "2.5")
		w.Header().Set("Set-Cookie", "private-cookie-value")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "{\"error\":\"overload\",\"resets_in_seconds\":9}")
	}))
	defer server.Close()

	client := testTransport(t, Deadlines{FirstByte: time.Second, Idle: time.Second, Total: 2 * time.Second, MaxBodyBytes: 1024})
	request := testRequest(server.URL)
	request.Prepare = func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("private-request"))
		if err == nil {
			req.Header.Set("Authorization", "Bearer private-token")
			req.Header.Set("session-id", "private-session-id")
			req.Header.Set("thread-id", "private-thread-id")
		}
		return req, err
	}
	_, err := client.Do(context.Background(), request)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("expected classified HTTP error, got %T %v", err, err)
	}
	if failure.Kind != KindOverload || failure.RetryAfterMS == nil || *failure.RetryAfterMS != 2500 {
		t.Fatalf("status/retry-after classification = %#v", failure)
	}
	if failure.PartialBody != nil {
		t.Fatal("HTTP error body must not become stream continuation progress")
	}
	if !reflect.DeepEqual(failure.Evidence.RoutingHeaderNames, []string{"Session-Id", "Thread-Id"}) {
		t.Fatalf("routing telemetry exposed wrong header names: %v", failure.Evidence.RoutingHeaderNames)
	}
	if failure.Evidence.BodyBytes != int64(len("private-request")) || failure.Evidence.EndpointHost == "" {
		t.Fatalf("request evidence is incomplete: %#v", failure.Evidence)
	}
	formatted := fmt.Sprintf("%+v %#v", failure, request)
	for _, private := range []string{"private-token", "private-cookie-value", "private-request", "private-session-id", "private-thread-id"} {
		if strings.Contains(formatted, private) {
			t.Fatalf("diagnostic leaked %q: %s", private, formatted)
		}
	}
}

func TestStandardDeadlinesMatchProviderContract(t *testing.T) {
	deadlines := StandardDeadlines()
	if deadlines.FirstByte != 120*time.Second || deadlines.Idle != 90*time.Second || deadlines.Total != 20*time.Minute || deadlines.MaxBodyBytes != 16_000_000 {
		t.Fatalf("unexpected standard deadlines: %#v", deadlines)
	}
}

func TestDeadlinesFromEnvironmentScalesOnlyTimeValues(t *testing.T) {
	t.Setenv("KOGEN_TIME_SCALE", "0.02")
	deadlines, err := DeadlinesFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if deadlines.FirstByte != 2400*time.Millisecond || deadlines.Idle != 1800*time.Millisecond || deadlines.Total != 24*time.Second || deadlines.MaxBodyBytes != 16_000_000 {
		t.Fatalf("scaled provider deadlines = %#v", deadlines)
	}
}

func TestDeadlinesFromEnvironmentRejectsInvalidScale(t *testing.T) {
	t.Setenv("KOGEN_TIME_SCALE", "not-a-number")
	if _, err := DeadlinesFromEnvironment(); err == nil {
		t.Fatal("invalid KOGEN_TIME_SCALE was accepted")
	}
}
