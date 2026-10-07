package journal

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"kogen-go/internal/contract"
	"kogen-go/internal/safefs"
)

func TestRunStoreCreatesDraftDefaultsAndRecordsEventBeforeSnapshot(t *testing.T) {
	store, root, directory := newTestStore(t)
	defer root.Close()

	initialBytes, err := root.ReadFile(directory + "/run.json")
	if err != nil {
		t.Fatal(err)
	}
	var initial map[string]json.RawMessage
	if err := json.Unmarshal(initialBytes, &initial); err != nil {
		t.Fatal(err)
	}
	if string(initial["landing"]) != "null" || string(initial["recovery"]) != "[]" || string(initial["cleanup_pending"]) != "false" {
		t.Fatalf("draft defaults missing: %s", initialBytes)
	}

	event := NewRunEvent("repair", 1234)
	if err := event.Set("reason", "repair_cap"); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(event, testSnapshot("failed")); err != nil {
		t.Fatal(err)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].TS != 1234 {
		t.Fatalf("unexpected journal events: %#v", events)
	}
	var reason string
	if err := json.Unmarshal(events[0].Fields["reason"], &reason); err != nil || reason != "repair_cap" {
		t.Fatalf("reason was not preserved as text: %q (%v)", reason, err)
	}
	current, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "failed" {
		t.Fatalf("snapshot status = %q, want failed", current.Status)
	}
	info, err := root.Lstat(directory + "/run.json")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestRecordCrashInjectionLeavesJournalAheadAndOldSnapshotIntact(t *testing.T) {
	store, root, _ := newTestStore(t)
	defer root.Close()
	crash := errors.New("simulated process death")
	event := NewRunEvent("finished", 9876)
	if err := event.Set("reason", "interrupted"); err != nil {
		t.Fatal(err)
	}
	err := store.RecordWithFaultInjector(event, testSnapshot("failed"), func(stage PublicationStage) error {
		if stage == AfterEventAppend {
			return crash
		}
		return nil
	})
	if !errors.Is(err, crash) {
		t.Fatalf("injected failure was not returned: %v", err)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Event != "finished" {
		t.Fatalf("durable append missing: %#v", events)
	}
	snapshot, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "running" {
		t.Fatalf("snapshot changed before its atomic publication: %q", snapshot.Status)
	}
}

func TestSnapshotPublicationFaultKeepsOldSnapshotAndPostPublishFaultKeepsNew(t *testing.T) {
	store, root, _ := newTestStore(t)
	defer root.Close()
	crash := errors.New("injected")
	err := store.RecordWithFaultInjector(NewRunEvent("finished", 5), testSnapshot("failed"), func(stage PublicationStage) error {
		if stage == BeforeSnapshotWrite {
			return crash
		}
		return nil
	})
	if !errors.Is(err, crash) {
		t.Fatalf("before-publication injection: %v", err)
	}
	current, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "running" {
		t.Fatalf("snapshot changed before publication: %q", current.Status)
	}

	err = store.RecordWithFaultInjector(NewRunEvent("finished", 6), testSnapshot("landed"), func(stage PublicationStage) error {
		if stage == AfterSnapshotWrite {
			return crash
		}
		return nil
	})
	if !errors.Is(err, crash) {
		t.Fatalf("after-publication injection: %v", err)
	}
	current, err = store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "landed" {
		t.Fatalf("published snapshot missing after injected crash: %q", current.Status)
	}
}

func TestRequestEvidenceAndTranscriptRedactWireSecretsAndUnknownUsageIsNull(t *testing.T) {
	body := []byte(`{"model":"gpt-6-luna","input":[{"role":"user","content":"prompt-do-not-journal"}]}`)
	request := contract.ProviderRequest{
		Endpoint: "https://user:password@example.invalid:8443/v1/responses?api_key=query-secret#frag",
		Headers: http.Header{
			"Authorization":      {"Bearer header-secret"},
			"Thread-Id":          {"thread-123"},
			"Session-Id":         {"cache-456"},
			"X-Codex-Turn-State": {"opaque-routing-secret"},
		},
		Body: body,
	}
	evidence, err := EvidenceFromRequest(request, RequestIdentity{
		CacheKey: "cache-456", ThreadID: "thread-123", Role: "builder",
		Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max",
	}, time.UnixMilli(100), time.UnixMilli(125), nil, len(body))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.EndpointHost != "example.invalid:8443" || evidence.EndpointPath != "/v1/responses" {
		t.Fatalf("unexpected endpoint evidence: %#v", evidence)
	}
	if strings.Join(evidence.RoutingHeaderNames, ",") != "session-id,thread-id,x-codex-turn-state" {
		t.Fatalf("unexpected routing header names: %#v", evidence.RoutingHeaderNames)
	}
	if !evidence.CodexTurnStatePresent || evidence.BodyBytes != int64(len(body)) {
		t.Fatalf("request size or routing-state presence lost: %#v", evidence)
	}
	record, err := TranscriptFromEvidence(evidence, 25)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(encoded)
	for _, secret := range []string{"prompt-do-not-journal", "header-secret", "opaque-routing-secret", "password", "query-secret", "api_key", "user:"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("transcript leaked %q: %s", secret, serialized)
		}
	}
	request.Endpoint = "https://example.invalid/v1/secret-token/response"
	redactedEvidence, err := EvidenceFromRequest(request, RequestIdentity{
		CacheKey: "cache-456", ThreadID: "thread-123", Role: "builder",
		Provider: "chatgpt", Model: "gpt-6-luna", Effort: "max",
	}, time.UnixMilli(100), time.UnixMilli(125), nil)
	if err != nil {
		t.Fatal(err)
	}
	if redactedEvidence.EndpointPath != "/v1/redacted/response" {
		t.Fatalf("credential-like path segment was retained: %q", redactedEvidence.EndpointPath)
	}
	if string(recordUsageJSON(t, record)) != "null" {
		t.Fatalf("unknown usage did not remain null: %s", recordUsageJSON(t, record))
	}

	store, root, _ := newTestStore(t)
	defer root.Close()
	if err := store.AppendTranscript(record); err != nil {
		t.Fatal(err)
	}
	transcript, err := root.ReadFile(store.directory + "/transcript.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(transcript), "prompt-do-not-journal") || !strings.HasSuffix(string(transcript), "\n") {
		t.Fatalf("unsafe or incomplete transcript: %q", transcript)
	}
}

func TestTranscriptPreservesMeasuredZeroAndRejectsUnsafeIdentifiers(t *testing.T) {
	zero := int64(0)
	evidence := contract.RequestEvidence{
		EndpointHost: "api.example.invalid", EndpointPath: "/v1/responses",
		RequestedAt: time.UnixMilli(1), RespondedAt: time.UnixMilli(2),
		BodyBytes: 2, CacheKey: "cache-456", ThreadID: "thread-123",
		Role: "shaper", Provider: "chatgpt", Model: "gpt-6.1-sol", Effort: "high",
		Usage: &contract.TokenUsage{Input: &zero},
	}
	record, err := TranscriptFromEvidence(evidence, 1)
	if err != nil {
		t.Fatal(err)
	}
	if record.Usage == nil || record.Usage.Input == nil || *record.Usage.Input != 0 || record.Usage.Output != nil {
		t.Fatalf("measured zero and unknown fields collapsed: %#v", record.Usage)
	}
	evidence.ThreadID = "https://secret.invalid/path"
	if _, err := TranscriptFromEvidence(evidence, 1); err == nil {
		t.Fatal("unsafe protocol identity was accepted")
	}
}

func TestAuditAndRecoveryRecordsCarryDraftSemantics(t *testing.T) {
	audit, err := AuditEvent(12, "R1", []AuditItem{{ID: "A1", Verdict: "contradicts", Reason: "advice only"}})
	if err != nil {
		t.Fatal(err)
	}
	encodedAudit, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	var auditJSON map[string]json.RawMessage
	if err := json.Unmarshal(encodedAudit, &auditJSON); err != nil {
		t.Fatal(err)
	}
	if string(auditJSON["mode"]) != `"observational"` || string(auditJSON["demoted"]) != "false" || string(auditJSON["advisory_items"]) != "[]" {
		t.Fatalf("audit is not observational: %s", encodedAudit)
	}
	if _, exists := auditJSON["acceptance_demoted"]; exists {
		t.Fatalf("reserved demotion event field was emitted: %s", encodedAudit)
	}

	tree, ref := strings.Repeat("b", 40), "refs/kogen/candidates/"+strings.Repeat("a", 32)+"/recovery-w1"
	recovery := RecoveryRecord{Workspace: "w1", Base: strings.Repeat("c", 40), Tree: &tree, Ref: &ref, Verification: "unverified"}
	if err := recovery.Validate(); err != nil {
		t.Fatal(err)
	}
	preserved, err := RecoveryPreservedEvent(14, recovery)
	if err != nil {
		t.Fatal(err)
	}
	encodedRecovery, err := json.Marshal(preserved)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encodedRecovery), `"verification":"unverified"`) || !strings.Contains(string(encodedRecovery), `"archive":null`) {
		t.Fatalf("recovery event lost draft identity: %s", encodedRecovery)
	}

	archive := "recovery/workspace.tar.manifest.json"
	invalid := recovery
	invalid.Archive = &archive
	if err := invalid.Validate(); err == nil {
		t.Fatal("mixed ref and archive preservation was accepted")
	}
}

func TestRecoveryAndCleanupHelpersPublishDraftFieldsWithEvents(t *testing.T) {
	store, root, _ := newTestStore(t)
	defer root.Close()
	tree, ref := strings.Repeat("b", 40), "refs/kogen/candidates/"+strings.Repeat("a", 32)+"/recovery-w1"
	recovery := RecoveryRecord{Workspace: "w1", Base: strings.Repeat("c", 40), Tree: &tree, Ref: &ref, Verification: "unverified"}
	snapshot, err := store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRecoveryPreserved(snapshot, 20, recovery); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Recovery) != 1 || snapshot.Recovery[0].Verification != "unverified" {
		t.Fatalf("recovery not persisted in run snapshot: %#v", snapshot.Recovery)
	}
	if err := store.RecordRecoveryPreserved(snapshot, 21, recovery); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCleanupFailure(snapshot, 22, "w2", []byte("archive publication failed")); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.CleanupPending || len(snapshot.Recovery) != 1 {
		t.Fatalf("cleanup pending or recovery state lost: %#v", snapshot)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Event != "recovery_preserved" || events[1].Event != "cleanup_failure" {
		t.Fatalf("repeated recovery emitted duplicate or cleanup missing: %#v", events)
	}
}

func TestAgentEventsContainOnlySafeLifecycleMetadata(t *testing.T) {
	store, root, directory := newTestStore(t)
	defer root.Close()
	if err := store.AppendAgentEvent(strings.Repeat("d", 32), AgentEvent{Event: "started", TS: 3, Role: "context", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	data, err := root.ReadFile(directory + "/agents/" + strings.Repeat("d", 32) + "/events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"event":"started","ts":3,"role":"context","status":"running"}`+"\n" {
		t.Fatalf("unexpected safe agent record: %s", data)
	}
	if err := store.AppendAgentEvent("../../outside", AgentEvent{Event: "started", TS: 4, Role: "context"}); err == nil {
		t.Fatal("path-bearing agent id was accepted")
	}
	if err := store.AppendAgentEvent(strings.Repeat("e", 32), AgentEvent{Event: "tool_started", TS: 4, Role: "builder", Status: "running"}); err != nil {
		t.Fatal(err)
	}
}

func TestSafePublicationReplacesSymlinkLeafWithoutFollowingIt(t *testing.T) {
	_, root, directory := newTestStore(t)
	defer root.Close()
	if err := root.PublishPrivate("outside", []byte("untouched"), safefs.PublicationCreateOnly); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("../../outside", directory+"/run-link.json"); err != nil {
		t.Fatal(err)
	}
	if err := root.PublishPrivate(directory+"/run-link.json", []byte("new"), safefs.PublicationReplace); err != nil {
		t.Fatal(err)
	}
	contents, err := root.ReadFile("outside")
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "untouched" {
		t.Fatalf("symlink target was modified: %q", contents)
	}
}

func TestSetTextEncodesNonUTF8ReasonBytes(t *testing.T) {
	event := NewRunEvent("cleanup_failure", 1)
	if err := event.SetText("detail", []byte{0xff, 0xfe}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	var detail string
	if err := json.Unmarshal(fields["detail_base64"], &detail); err != nil {
		t.Fatal(err)
	}
	if detail != "//4=" {
		t.Fatalf("unexpected base64 detail %q", detail)
	}
	if _, ok := fields["detail"]; ok {
		t.Fatalf("non-UTF8 bytes were written as text: %s", encoded)
	}
	if _, err := base64.StdEncoding.DecodeString(detail); err != nil {
		t.Fatal(err)
	}
}

func newTestStore(t *testing.T) (*RunStore, *safefs.Root, string) {
	t.Helper()
	root, err := safefs.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := "runs/" + strings.Repeat("a", 32)
	store, err := NewRunStore(root, directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(testSnapshot("running")); err != nil {
		root.Close()
		t.Fatal(err)
	}
	return store, root, directory
}

func testSnapshot(status string) RunSnapshot {
	return RunSnapshot{
		Schema: 2, RunID: strings.Repeat("a", 32), Slug: "greet",
		ApprovalSHA256: strings.Repeat("b", 64), ApprovalCommit: strings.Repeat("c", 40),
		TargetBranch: "main", Status: status, OwnerPID: 7, OwnerStartedMS: 1, StartedMS: 2,
	}
}

func recordUsageJSON(t *testing.T, record TranscriptRecord) []byte {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	return object["usage"]
}
