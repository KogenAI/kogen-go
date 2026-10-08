package witness

import (
	"encoding/json"

	"kogen-go/internal/shape/validate"
)

// Verdict is an adjudication for one failed witness acceptance assertion.
type Verdict string

const (
	VerdictTestWrong    Verdict = "TEST-WRONG"
	VerdictWitnessWrong Verdict = "WITNESS-WRONG"
	VerdictUndecided    Verdict = "UNDECIDED"
)

// Judgment preserves the test auditor's citation and reason. Unusable or
// missing model rows are represented as UNDECIDED rather than discarded.
type Judgment struct {
	ID       string  `json:"id"`
	Verdict  Verdict `json:"verdict"`
	Citation string  `json:"citation"`
	Reason   string  `json:"reason"`
}

const testAuditorMarker = "You are Kogen's acceptance test auditor."

// AdjudicatorInstructions is the single tool-less test-auditor prompt used for
// witness red adjudication. It does not grant the Build auditor's advice any
// authority over the immutable gate result.
func AdjudicatorInstructions() string {
	return testAuditorMarker + " For each failing witness assertion, decide whether the test is wrong, the witness implementation is wrong, or the evidence is insufficient. Reply with JSON only in this form: {\"items\":[{\"id\":\"A1\",\"verdict\":\"TEST-WRONG|WITNESS-WRONG|UNDECIDED\",\"citation\":\"…\",\"reason\":\"…\"}]}."
}

// DecodeAdjudications returns one judgment for every requested failed ID, in
// the same order. Unknown IDs are ignored. An invalid, missing, or duplicate
// row becomes UNDECIDED and therefore cannot suppress an acceptance failure.
func DecodeAdjudications(raw []byte, failedIDs []string) []Judgment {
	var response struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return undecidedRows(failedIDs)
	}

	expected := make(map[string]struct{}, len(failedIDs))
	for _, id := range failedIDs {
		if id != "" {
			expected[id] = struct{}{}
		}
	}
	rows := make(map[string]Judgment, len(expected))
	duplicates := make(map[string]struct{})
	for _, encoded := range response.Items {
		var row struct {
			ID       string  `json:"id"`
			Verdict  string  `json:"verdict"`
			Citation *string `json:"citation"`
			Reason   *string `json:"reason"`
		}
		if err := json.Unmarshal(encoded, &row); err != nil {
			continue
		}
		if _, ok := expected[row.ID]; !ok {
			continue
		}
		verdict := Verdict(row.Verdict)
		if (verdict != VerdictTestWrong && verdict != VerdictWitnessWrong && verdict != VerdictUndecided) ||
			row.Citation == nil || row.Reason == nil {
			continue
		}
		if _, already := rows[row.ID]; already {
			delete(rows, row.ID)
			duplicates[row.ID] = struct{}{}
			continue
		}
		if _, duplicate := duplicates[row.ID]; duplicate {
			continue
		}
		rows[row.ID] = Judgment{ID: row.ID, Verdict: verdict, Citation: *row.Citation, Reason: *row.Reason}
	}

	result := make([]Judgment, 0, len(failedIDs))
	seen := make(map[string]struct{}, len(failedIDs))
	for _, id := range failedIDs {
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		if _, duplicate := duplicates[id]; duplicate {
			result = append(result, unusableJudgment(id))
			continue
		}
		if judgment, ok := rows[id]; ok {
			result = append(result, judgment)
		} else {
			result = append(result, unusableJudgment(id))
		}
	}
	return result
}

func undecidedRows(ids []string) []Judgment {
	result := make([]Judgment, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, unusableJudgment(id))
	}
	return result
}

func unusableJudgment(id string) Judgment {
	return Judgment{
		ID: id, Verdict: VerdictUndecided,
		Reason: "The auditor did not provide a usable adjudication.",
	}
}

func repairPlan(judgments []Judgment) ([]RepairItem, []validate.Warning) {
	repairs := make([]RepairItem, 0, len(judgments))
	warnings := make([]validate.Warning, 0)
	for _, judgment := range judgments {
		switch judgment.Verdict {
		case VerdictTestWrong:
			repairs = append(repairs, RepairItem{
				ID: judgment.ID, Scope: RepairTest, Verdict: judgment.Verdict,
				Citation: judgment.Citation, Reason: judgment.Reason,
			})
		case VerdictWitnessWrong:
			repairs = append(repairs, RepairItem{
				ID: judgment.ID, Scope: RepairWitness, Verdict: judgment.Verdict,
				Citation: judgment.Citation, Reason: judgment.Reason,
			})
		case VerdictUndecided:
			warnings = append(warnings, validate.Warning{
				Code: "feasibility_concern", ItemIDs: []string{judgment.ID}, Message: judgment.Reason,
			})
		}
	}
	return repairs, warnings
}
