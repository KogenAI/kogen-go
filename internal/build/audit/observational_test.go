package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"kogen-go/internal/contract"
)

func TestOverStrictAndContradictsStayObservationalInAuditReceipt(t *testing.T) {
	for _, verdict := range []Verdict{VerdictOverStrict, VerdictContradicts} {
		t.Run(string(verdict), func(t *testing.T) {
			transport := &fakeTransport{reply: `{"items":[{"id":"A2","verdict":"` + string(verdict) + `","reason":"advice only"}]}`}
			manifest := auditManifest()
			auditor, err := New(manifest, transport)
			if err != nil {
				t.Fatal(err)
			}

			input := validInput(t, "R1", manifest.Effective[contract.RoleName("auditor")])
			input.FailedItemIDs = []string{"A2"}
			result, err := auditor.BeforeRepair(context.Background(), input, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Audited || len(result.Receipt.Items) != 1 || result.Receipt.Items[0].ID != "A2" || result.Receipt.Items[0].Verdict != verdict {
				t.Fatalf("audit did not retain the A2 observation: %+v", result)
			}
			if result.Receipt.Mode != ModeObservational || result.Receipt.Demoted || result.Receipt.AdvisoryItems == nil || len(result.Receipt.AdvisoryItems) != 0 {
				t.Fatalf("audit reply changed observational policy fields: %+v", result.Receipt)
			}
			if feedback := result.AppendRepairAdvice("acceptance A2: fail"); !strings.Contains(feedback, "every approved acceptance item remains required") || !strings.Contains(feedback, "A2 "+string(verdict)+": advice only") {
				t.Fatalf("repair advice did not preserve A2's required status: %q", feedback)
			}

			event, err := result.Event(17)
			if err != nil {
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
			var mode string
			var demoted bool
			var advisory []string
			if err := json.Unmarshal(fields["mode"], &mode); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(fields["demoted"], &demoted); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(fields["advisory_items"], &advisory); err != nil {
				t.Fatal(err)
			}
			if mode != ModeObservational || demoted || advisory == nil || len(advisory) != 0 {
				t.Fatalf("audit event is not observational: %s", encoded)
			}
			if _, exists := fields["acceptance_demoted"]; exists {
				t.Fatalf("reserved demotion event leaked into audit receipt: %s", encoded)
			}
		})
	}
}
