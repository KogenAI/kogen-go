package queuestatus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"kogen-go/internal/queue/schedule"
	"kogen-go/internal/xspec/protocol"
)

func TestFactoriesExposeOnlyQueueAndStatusSlices(t *testing.T) {
	factories := Factories()
	if len(factories) != 2 || factories["queue"] == nil || factories["status"] == nil {
		t.Fatalf("factory registry = %#v, want queue and status", factories)
	}
	if factories["intent"] != nil || factories["approve"] != nil {
		t.Fatalf("queue/status registry unexpectedly claims another slice: %#v", factories)
	}
}

func TestQueueSliceUsesProductionSchedulerAndRealOwnerLock(t *testing.T) {
	ctx := context.Background()
	adapter, err := QueueFactory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slice := adapter.(*queueSlice)
	t.Cleanup(func() { _ = slice.Close() })
	applyQueue(t, ctx, slice, "Enqueue", `{"slug":"alpha","time":1,"priority":0}`)
	applyQueue(t, ctx, slice, "Start", "")
	if got := observeQueue(t, ctx, slice); got.Line != "building" || !got.Held || !got.Alive || got.Current != "alpha" {
		t.Fatalf("start observation = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(slice.root, "queue.pid")); err != nil {
		t.Fatalf("production queue lock was not created: %v", err)
	}
	applyQueue(t, ctx, slice, "Start", "")
	if got := observeQueue(t, ctx, slice); got.Line != "already_running" || got.Current != "alpha" {
		t.Fatalf("second start observation = %+v", got)
	}
	applyQueue(t, ctx, slice, "Outcome", `{"kind":"landed"}`)
	got := observeQueue(t, ctx, slice)
	want := schedule.QueueObservation{
		Last: "ok", Line: "done", Exit: 0, Held: false, Alive: false, Stop: false,
		Phase: "idle", Current: "", Queue: []string{}, Built: 1, Landed: 1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("completed queue observation = %+v, want %+v", got, want)
	}
	if _, err := os.Lstat(filepath.Join(slice.root, "queue.pid")); !os.IsNotExist(err) {
		t.Fatalf("queue lock remained after the production scheduler completed: %v", err)
	}
}

func TestQueueSliceReclaimsADeadOwnerThroughTheProductionLock(t *testing.T) {
	ctx := context.Background()
	adapter, err := QueueFactory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slice := adapter.(*queueSlice)
	t.Cleanup(func() { _ = slice.Close() })
	applyQueue(t, ctx, slice, "Enqueue", `{"slug":"alpha","time":1,"priority":0}`)
	applyQueue(t, ctx, slice, "Start", "")
	applyQueue(t, ctx, slice, "Die", "")
	if got := observeQueue(t, ctx, slice); got.Line != "owner_dead" || !got.Held || got.Alive {
		t.Fatalf("dead owner observation = %+v", got)
	}
	applyQueue(t, ctx, slice, "Start", "")
	if got := observeQueue(t, ctx, slice); got.Line != "building" || !got.Alive || got.Current != "alpha" {
		t.Fatalf("takeover observation = %+v", got)
	}
}

func TestStatusSliceUsesProductionDerivationAndEmitsFullObservation(t *testing.T) {
	ctx := context.Background()
	adapter, err := StatusFactory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slice := adapter.(*statusSlice)
	t.Cleanup(func() { _ = slice.Close() })

	applyStatus(t, ctx, slice, "Raw", `{"slug":"alpha","trailer":false,"claimed":true,"runStatus":"running","event":"interrupted","alive":false,"approved":true,"reason":"","same":true,"blocks":"","priority":1,"at":1}`)
	applyStatus(t, ctx, slice, "Raw", `{"slug":"bravo","trailer":false,"claimed":false,"runStatus":"","event":"","alive":false,"approved":true,"reason":"","same":true,"blocks":"alpha","priority":0,"at":2}`)
	got := observeStatus(t, ctx, slice)
	if got.Alpha != "interrupted" || got.Bravo != "blocked" || got.WhyB != "waiting for delivered dependencies: alpha" {
		t.Fatalf("derived status = %+v", got)
	}
	wantSections := []string{"Blocked", "Interrupted"}
	if !reflect.DeepEqual(got.Sections, wantSections) {
		t.Fatalf("sections = %v, want %v", got.Sections, wantSections)
	}
	if got.Queue == nil || got.Sections == nil {
		t.Fatalf("canonical empty lists must be [] rather than null: %+v", got)
	}
	if got.Exit != 0 || got.JSONDetail {
		t.Fatalf("initial watch state leaked into observation: %+v", got)
	}

	var fields map[string]json.RawMessage
	raw, err := marshalObservation(got)
	if err != nil || json.Unmarshal(raw, &fields) != nil {
		t.Fatalf("decode full observation: %v", err)
	}
	wantKeys := []string{
		"last", "alpha", "bravo", "charlie", "queue", "whyA", "whyB", "whyC", "sections", "earlier", "elapsed",
		"queueLine", "next", "nextPriority", "nextDependencies", "landedShown", "watchSlug", "watchStatus",
		"watchPosition", "watchQueueSize", "exit", "jsonDetail",
	}
	var gotKeys []string
	for key := range fields {
		gotKeys = append(gotKeys, key)
	}
	sortStrings(gotKeys)
	sortStrings(wantKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("observation keys = %v, want all canonical fields %v", gotKeys, wantKeys)
	}
}

func TestStatusQueueAndWatchUseProductionOwnerAndRenderTransitions(t *testing.T) {
	ctx := context.Background()
	adapter, err := StatusFactory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slice := adapter.(*statusSlice)
	t.Cleanup(func() { _ = slice.Close() })
	applyStatus(t, ctx, slice, "Row", `{"slug":"alpha","status":"building","priority":0,"at":0,"blocks":"","sched":"","started":70,"index":0}`)
	applyStatus(t, ctx, slice, "Queue", `{"running":true}`)
	applyStatus(t, ctx, slice, "Now", `{"t":100}`)
	applyStatus(t, ctx, slice, "Watch", `{"slug":""}`)
	got := observeStatus(t, ctx, slice)
	if got.Alpha != "building" || got.Elapsed != "s" || got.QueueLine != "running" || got.Exit != -1 {
		t.Fatalf("live owner/watch observation = %+v", got)
	}
	if slice.queuePID == nil || *slice.queuePID <= 0 {
		t.Fatalf("running queue has no observed process owner: pid=%v", slice.queuePID)
	}
	if _, err := os.Stat(filepath.Join(slice.root, "queue.pid")); err != nil {
		t.Fatalf("status queue event did not create the production lock: %v", err)
	}
	applyStatus(t, ctx, slice, "Queue", `{"running":false}`)
	applyStatus(t, ctx, slice, "Raw", `{"slug":"alpha","trailer":true,"claimed":false,"runStatus":"","event":"","alive":false,"approved":true,"reason":"","same":true,"blocks":"","priority":0,"at":0}`)
	applyStatus(t, ctx, slice, "Watch", `{"slug":"alpha"}`)
	got = observeStatus(t, ctx, slice)
	if got.Alpha != "landed" || got.WatchStatus != "landed" || got.Exit != 0 {
		t.Fatalf("landed watch observation = %+v", got)
	}
}

func applyQueue(t *testing.T, ctx context.Context, slice *queueSlice, tag, value string) {
	t.Helper()
	event := protocol.Event{Tag: tag}
	if value != "" {
		event.Value = json.RawMessage(value)
		event.HasValue = true
	}
	if err := slice.Apply(ctx, event); err != nil {
		t.Fatalf("apply queue %s: %v", tag, err)
	}
}

func observeQueue(t *testing.T, ctx context.Context, slice *queueSlice) schedule.QueueObservation {
	t.Helper()
	raw, err := slice.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var observation schedule.QueueObservation
	if err := json.Unmarshal(raw, &observation); err != nil {
		t.Fatalf("decode queue observation: %v", err)
	}
	return observation
}

func applyStatus(t *testing.T, ctx context.Context, slice *statusSlice, tag, value string) {
	t.Helper()
	event := protocol.Event{Tag: tag}
	if value != "" {
		event.Value = json.RawMessage(value)
		event.HasValue = true
	}
	if err := slice.Apply(ctx, event); err != nil {
		t.Fatalf("apply status %s: %v", tag, err)
	}
}

func observeStatus(t *testing.T, ctx context.Context, slice *statusSlice) statusObservation {
	t.Helper()
	raw, err := slice.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var observation statusObservation
	if err := json.Unmarshal(raw, &observation); err != nil {
		t.Fatalf("decode status observation: %v", err)
	}
	return observation
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
