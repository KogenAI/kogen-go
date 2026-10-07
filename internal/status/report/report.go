package report

import (
	"encoding/json"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"kogen-go/internal/status/derive"
)

// Agent is a live agent row collected by the status observer.
type Agent struct {
	ID        string
	Role      string
	Build     string
	Status    string
	ElapsedMS int64
	Activity  string
	Events    string
}

// Event is one decoded journal event. The event envelope is stripped from
// report payloads, while the original order is retained.
type Event map[string]any

// RunData supplements the small run fact used by derivation with report-only
// journal data. CandidateDiffPath is non-empty only when candidate.diff exists.
type RunData struct {
	Snapshot          map[string]any
	Events            []Event
	CandidateDiffPath string
}

// Report is one immutable status observation ready for rendering. Board rows
// are expected to come from derive.Derive; the other fields are observations,
// not precomputed public output.
type Report struct {
	Board     derive.Board
	Agents    []Agent
	QueuePID  *int
	NowMS     int64
	StateRoot string
	Approvals map[string]any
	Runs      map[string]RunData
}

// Build assembles the §2.10 JSON object for one Intent. Missing journal facts
// remain null (or empty arrays where the report schema specifies a list).
func Build(row derive.Row, status Report) map[string]any {
	var run *derive.Run
	if row.Run != nil {
		run = row.Run
	}
	var data RunData
	if run != nil {
		data = status.Runs[run.ID]
	}
	events := data.Events
	started := firstEvent(events, "started")
	finished := lastEvent(events, "finished")
	verification := lastEvent(events, "verification")
	modelStages := eventPayloads(events, "model_stage")
	approval := status.Approvals[row.Intent.Slug]
	base := field(approval, "base_sha")
	if base == nil {
		base = field(started, "base_sha")
	}
	var landedSHA any
	if row.Landing != nil {
		landedSHA = row.Landing.Commit
	}
	var runID any
	var journal any
	var usedMS, pausedMS int64
	if run != nil {
		runID = run.ID
		journal = filepath.Join(status.StateRoot, "runs", run.ID)
		endMS := status.NowMS
		if value, ok := integer(finished, "ts"); ok {
			endMS = value
		}
		usedMS = max(0, endMS-run.StartedAt)
		for _, event := range events {
			if eventName(event) == "provider_wait" {
				if value, ok := integer(event, "paused_ms"); ok {
					pausedMS += value
				}
			}
		}
	}
	credential := map[string]any{
		"source": field(started, "credential_source"),
		"label":  field(started, "credential_label"),
	}
	landPolicy := field(started, "land")
	if landPolicy == nil {
		landPolicy = "green"
	}
	budgetMS := field(started, "budget_ms")
	if budgetMS == nil {
		budgetMS = field(data.Snapshot, "budget_ms")
	}
	priority := row.Intent.Priority
	blocksOn := row.Intent.Dependencies
	if blocksOn == nil {
		blocksOn = []string{}
	}
	result := map[string]any{
		"slug":           row.Intent.Slug,
		"status":         string(row.Status),
		"build_id":       runID,
		"journal":        journal,
		"verdict":        field(finished, "verdict"),
		"land_policy":    landPolicy,
		"advisory_items": valueOr(field(finished, "advisory_items"), []any{}),
		"approval":       valueOr(approval, nil),
		"approved_by":    field(approval, "by"),
		"base":           base,
		"candidate":      payload(firstEvent(events, "commit_result")),
		"landed_sha":     landedSHA,
		"priority":       priority,
		"blocks_on":      blocksOn,
		"cache_hit_rate": cacheHitRate(modelStages),
		"credential":     credential,
		"rungs":          rungRows(events),
		"best_candidate": bestCandidate(events, status.StateRoot, run),
		"audit":          auditRows(events),
		"acceptance":     valueOr(field(verification, "acceptance"), []any{}),
		"checks":         projectChecks(field(verification, "checks")),
		"model_stages":   modelStages,
		"findings":       findings(field(verification, "checks")),
		"failures":       valueOr(field(finished, "failures"), []any{}),
		"sandbox":        field(started, "sandbox"),
		"budget": map[string]any{
			"budget_ms": budgetMS,
			"used_ms":   usedMS,
			"paused_ms": pausedMS,
		},
	}
	if agents := agentRows(status.Agents); len(agents) != 0 {
		result["agents"] = agents
	}
	return result
}

// JSON serializes the §2.10 object with stable map-key ordering and without
// HTML escaping. The returned bytes do not include a trailing newline.
func JSON(row derive.Row, status Report) ([]byte, error) {
	var output strings.Builder
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(Build(row, status)); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(output.String(), "\n")), nil
}

func agentRows(agents []Agent) []map[string]any {
	if len(agents) == 0 {
		return nil
	}
	agents = append([]Agent(nil), agents...)
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Build != agents[j].Build {
			return agents[i].Build < agents[j].Build
		}
		return agents[i].ID < agents[j].ID
	})
	rows := make([]map[string]any, 0, len(agents))
	for _, agent := range agents {
		rows = append(rows, agentRow(agent))
	}
	return rows
}

// AgentRow returns the JSONL/report representation shared by overview agents
// and the Build report's optional agents field.
func AgentRow(agent Agent) map[string]any { return agentRow(agent) }

func agentRow(agent Agent) map[string]any {
	return map[string]any{
		"type":       "agent",
		"id":         agent.ID,
		"role":       agent.Role,
		"build":      agent.Build,
		"status":     agent.Status,
		"elapsed_ms": agent.ElapsedMS,
		"activity":   agent.Activity,
		"events":     agent.Events,
	}
}

func rungRows(events []Event) []map[string]any {
	starts := matchingEvents(events, "rung_started")
	rows := make([]map[string]any, 0, len(starts))
	for _, started := range starts {
		rung := stringValue(field(started, "rung"))
		finished := matchingRung(events, "rung_finished", rung)
		stages := make([]Event, 0)
		for _, event := range matchingEvents(events, "model_stage") {
			if stringValue(field(event, "rung")) == rung {
				stages = append(stages, event)
			}
		}
		reason := field(finished, "reason")
		if reason == nil {
			reason = field(started, "entered_because")
		}
		wallMS := field(started, "wall_ms")
		if wallMS == nil {
			wallMS = int64(0)
		}
		rows = append(rows, map[string]any{
			"rung":          valueOr(field(started, "rung"), ""),
			"model":         field(started, "model"),
			"effort":        field(started, "effort"),
			"reason":        reason,
			"verdict":       field(finished, "verdict"),
			"diff_lines":    field(finished, "diff_lines"),
			"candidate_ref": field(finished, "candidate_ref"),
			"wall_ms":       wallMS,
			"tokens":        sumTokens(stages),
		})
	}
	return rows
}

func sumTokens(stages []Event) map[string]any {
	keys := []string{"input", "cached_input", "cache_write", "output", "reasoning"}
	totals := make(map[string]any, len(keys))
	for _, key := range keys {
		var sum int64
		known := len(stages) != 0
		for _, stage := range stages {
			value, ok := integer(field(stage, "tokens"), key)
			if !ok {
				known = false
				break
			}
			sum += value
		}
		if known {
			totals[key] = sum
		} else {
			totals[key] = nil
		}
	}
	return totals
}

func bestCandidate(events []Event, stateRoot string, run *derive.Run) any {
	if run == nil {
		return nil
	}
	candidates := matchingEvents(events, "rung_finished")
	selected := ""
	if selection := firstEvent(events, "selection"); selection != nil {
		selected = stringValue(field(selection, "winner_rung"))
	}
	var chosen Event
	if selected != "" {
		chosen = matchingRung(events, "rung_finished", selected)
	}
	if chosen == nil {
		for i := len(candidates) - 1; i >= 0; i-- {
			if value, exists := candidates[i]["candidate_ref"]; exists && value != nil {
				chosen = candidates[i]
				break
			}
		}
	}
	if chosen == nil {
		return nil
	}
	return map[string]any{
		"rung":      field(chosen, "rung"),
		"ref":       field(chosen, "candidate_ref"),
		"diff_path": filepath.Join(stateRoot, "runs", run.ID, "candidate.diff"),
		"verdict":   field(chosen, "verdict"),
	}
}

func auditRows(events []Event) []map[string]any {
	rows := make([]map[string]any, 0)
	for _, event := range matchingEvents(events, "audit") {
		items, ok := field(event, "items").([]any)
		if !ok {
			continue
		}
		for _, raw := range items {
			rows = append(rows, map[string]any{
				"rung":    field(event, "rung"),
				"id":      field(raw, "id"),
				"verdict": field(raw, "verdict"),
				"reason":  field(raw, "reason"),
			})
		}
	}
	return rows
}

func projectChecks(raw any) []map[string]any {
	checks, ok := raw.([]any)
	if !ok {
		return []map[string]any{}
	}
	rows := make([]map[string]any, 0, len(checks))
	for _, check := range checks {
		rows = append(rows, map[string]any{
			"name":    field(check, "name"),
			"status":  field(check, "status"),
			"excused": valueOr(field(check, "excused"), false),
		})
	}
	return rows
}

func findings(raw any) []any {
	checks, ok := raw.([]any)
	if !ok {
		return []any{}
	}
	rows := make([]any, 0)
	for _, check := range checks {
		items, ok := field(check, "findings").([]any)
		if ok {
			rows = append(rows, items...)
		}
	}
	return rows
}

func cacheHitRate(stages []any) any {
	if len(stages) == 0 {
		return nil
	}
	var input, cached int64
	for _, stage := range stages {
		tokens := field(stage, "tokens")
		currentInput, inputOK := integer(tokens, "input")
		currentCached, cachedOK := integer(tokens, "cached_input")
		if !inputOK || !cachedOK {
			return nil
		}
		input += currentInput
		cached += currentCached
	}
	total := input + cached
	if total == 0 {
		return nil
	}
	return float64(cached) / float64(total)
}

func matchingRung(events []Event, name, rung string) Event {
	for _, event := range matchingEvents(events, name) {
		if stringValue(field(event, "rung")) == rung {
			return event
		}
	}
	return nil
}

func matchingEvents(events []Event, name string) []Event {
	rows := make([]Event, 0)
	for _, event := range events {
		if eventName(event) == name {
			rows = append(rows, event)
		}
	}
	return rows
}

func firstEvent(events []Event, name string) Event {
	for _, event := range events {
		if eventName(event) == name {
			return event
		}
	}
	return nil
}

func lastEvent(events []Event, name string) Event {
	for i := len(events) - 1; i >= 0; i-- {
		if eventName(events[i]) == name {
			return events[i]
		}
	}
	return nil
}

func eventPayloads(events []Event, name string) []any {
	rows := make([]any, 0)
	for _, event := range matchingEvents(events, name) {
		rows = append(rows, payload(event))
	}
	return rows
}

func payload(event Event) map[string]any {
	if event == nil {
		return nil
	}
	row := make(map[string]any, len(event))
	for key, value := range event {
		if key == "event" || key == "ts" {
			continue
		}
		row[key] = value
	}
	return row
}

func eventName(event Event) string { return stringValue(field(event, "event")) }

func field(value any, key string) any {
	if fields, ok := value.(map[string]any); ok {
		return fields[key]
	}
	if fields, ok := value.(Event); ok {
		return fields[key]
	}
	return nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func integer(value any, keys ...string) (int64, bool) {
	current := value
	for _, key := range keys {
		current = field(current, key)
	}
	switch number := current.(type) {
	case int:
		return int64(number), true
	case int8:
		return int64(number), true
	case int16:
		return int64(number), true
	case int32:
		return int64(number), true
	case int64:
		return number, true
	case uint:
		return int64(number), true
	case uint8:
		return int64(number), true
	case uint16:
		return int64(number), true
	case uint32:
		return int64(number), true
	case uint64:
		if number <= uint64(^uint64(0)>>1) {
			return int64(number), true
		}
	case json.Number:
		parsed, err := number.Int64()
		return parsed, err == nil
	case float64:
		if math.Trunc(number) != number || math.IsNaN(number) || math.IsInf(number, 0) || number < math.MinInt64 || number >= math.MaxInt64 {
			return 0, false
		}
		return int64(number), true
	}
	return 0, false
}

func valueOr(value any, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}
