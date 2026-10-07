package render

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"kogen-go/internal/status/derive"
	"kogen-go/internal/status/report"
)

// JSONLines emits one compact object per Intent in slug order, followed by the
// live agent rows. It produces no bytes when neither Intents nor agents exist.
func JSONLines(status report.Report) (string, error) {
	rows := append([]derive.Row(nil), status.Board.Rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Intent.Slug < rows[j].Intent.Slug })
	var output strings.Builder
	for _, row := range rows {
		var buildID, landedSHA any
		blocksOn := row.Intent.Dependencies
		if blocksOn == nil {
			blocksOn = []string{}
		}
		if row.Run != nil {
			buildID = row.Run.ID
		}
		if row.Landing != nil {
			landedSHA = row.Landing.Commit
		}
		encoded, err := marshal(map[string]any{
			"slug":       row.Intent.Slug,
			"status":     string(row.Status),
			"build_id":   buildID,
			"landed_sha": landedSHA,
			"priority":   row.Intent.Priority,
			"blocks_on":  blocksOn,
		})
		if err != nil {
			return "", err
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	for _, agent := range sortedAgents(status.Agents) {
		encoded, err := marshal(report.AgentRow(agent))
		if err != nil {
			return "", err
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	return output.String(), nil
}

// Overview renders the project status board, including queue order and live
// agents. Rows are stable even if the caller's observation slices are not.
func Overview(status report.Report) string {
	rows := sortedRows(status.Board.Rows)
	var output strings.Builder
	queue := status.Board.Queue
	if status.QueuePID != nil {
		fmt.Fprintf(&output, "Queue: running (pid %d)\n", *status.QueuePID)
	} else if len(queue) > 0 {
		fmt.Fprintf(&output, "Queue: stopped, %d waiting; start it with kogen queue start\n", len(queue))
	} else {
		output.WriteString("Queue: stopped\n")
	}
	if status.QueuePID == nil && len(queue) > 0 {
		if row, ok := findRow(rows, queue[0]); ok {
			dependencies := "no dependencies"
			if len(row.Intent.Dependencies) != 0 {
				dependencies = "dependencies delivered"
			}
			fmt.Fprintf(&output, "Next: %s (priority %d; %s; ties by approval time and slug)\n", queue[0], row.Intent.Priority, dependencies)
		}
	}
	if len(rows) == 0 {
		output.WriteString("No Intents.\n")
		renderAgents(&output, status.Agents)
		return output.String()
	}

	writeSection(&output, "Building", rows, derive.Building, status)
	if len(queue) > 0 {
		output.WriteString("Queued:\n")
		for _, slug := range queue {
			fmt.Fprintf(&output, "  %s\n", slug)
		}
	}
	writeSection(&output, "Blocked", rows, derive.Blocked, status)
	writeSection(&output, "Failed", rows, derive.Failed, status)
	writeSection(&output, "Parked", rows, derive.Parked, status)
	writeSection(&output, "Interrupted", rows, derive.Interrupted, status)
	writeSection(&output, "Drafts", rows, derive.Draft, status)
	writeLanded(&output, rows)
	renderAgents(&output, status.Agents)
	return output.String()
}

// Slug renders one Intent's text detail. The caller resolves unknown slugs
// before calling this function.
func Slug(row derive.Row, status report.Report) string {
	var output strings.Builder
	switch row.Status {
	case derive.Landed:
		fmt.Fprintf(&output, "%s: landed %s\n", row.Intent.Slug, shortID(landingID(row)))
	case derive.Approved:
		position := 0
		for i, slug := range status.Board.Queue {
			if slug == row.Intent.Slug {
				position = i + 1
				break
			}
		}
		fmt.Fprintf(&output, "%s: queued, %d of %d\n", row.Intent.Slug, position, len(status.Board.Queue))
	case derive.Building:
		fmt.Fprintf(&output, "%s: building, %s\n", row.Intent.Slug, buildingDetail(row, status))
	case derive.Blocked:
		reason := row.WaitReason
		if reason == "" {
			reason = "blocked"
		}
		fmt.Fprintf(&output, "%s: %s\n", row.Intent.Slug, reason)
	case derive.Draft:
		fmt.Fprintf(&output, "%s: draft; review it with kogen intent approve %s\n", row.Intent.Slug, row.Intent.Slug)
	case derive.Failed, derive.Parked, derive.Interrupted:
		fmt.Fprintf(&output, "%s: %s, %s\n", row.Intent.Slug, row.Status, reason(row))
	default:
		fmt.Fprintf(&output, "%s: %s\n", row.Intent.Slug, row.Status)
	}
	if row.Run != nil {
		fmt.Fprintf(&output, "Build %s: %s", shortID(row.Run.ID), row.Run.Status)
		if row.Run.Reason != "" {
			fmt.Fprintf(&output, ", %s", row.Run.Reason)
		}
		output.WriteByte('\n')
		runData := status.Runs[row.Run.ID]
		appendBuildDetails(&output, runData.Events)
		if runData.CandidateDiffPath != "" {
			fmt.Fprintf(&output, "  candidate diff: %s\n", runData.CandidateDiffPath)
		}
		fmt.Fprintf(&output, "  journal: %s\n", runPath(status, row.Run.ID))
	}
	return output.String()
}

// Watcher detects changes to the rendered frame and adds the required blank
// line before every changed frame after the first.
type Watcher struct {
	previous string
	seen     bool
}

// Next returns the bytes to write and whether the frame changed.
func (watcher *Watcher) Next(frame string) (string, bool) {
	if watcher.seen && watcher.previous == frame {
		return "", false
	}
	separator := ""
	if watcher.seen {
		separator = "\n"
	}
	watcher.seen = true
	watcher.previous = frame
	return separator + frame, true
}

// Idle reports whether watch may terminate: no queue owner, no Build in
// progress, and no running or waiting agent remains.
func Idle(status report.Report) bool {
	if status.QueuePID != nil {
		return false
	}
	for _, row := range status.Board.Rows {
		if row.Status == derive.Building {
			return false
		}
	}
	for _, agent := range status.Agents {
		if agent.Status == "running" || agent.Status == "waiting" {
			return false
		}
	}
	return true
}

// WatchExitCode returns the terminal watch status for a slug. It is intended
// to be used only after Idle returns true.
func WatchExitCode(status report.Report, slug string) int {
	if slug == "" {
		return 0
	}
	if row, ok := findRow(status.Board.Rows, slug); ok && row.Status == derive.Landed {
		return 0
	}
	return 1
}

func writeSection(output *strings.Builder, title string, rows []derive.Row, kind derive.Kind, status report.Report) {
	section := make([]derive.Row, 0)
	width := 0
	for _, row := range rows {
		if row.Status == kind {
			section = append(section, row)
			if len(row.Intent.Slug) > width {
				width = len(row.Intent.Slug)
			}
		}
	}
	if len(section) == 0 {
		return
	}
	fmt.Fprintf(output, "%s:\n", title)
	for _, row := range section {
		detail := ""
		switch kind {
		case derive.Building:
			detail = buildingDetail(row, status)
		case derive.Blocked:
			detail = row.WaitReason
			if detail == "" {
				detail = "blocked"
			}
		case derive.Failed, derive.Parked, derive.Interrupted:
			detail = fmt.Sprintf("%s (Build %s)", reason(row), shortID(runID(row)))
		}
		if detail == "" {
			fmt.Fprintf(output, "  %s\n", row.Intent.Slug)
		} else {
			fmt.Fprintf(output, "  %-*s  %s\n", width, row.Intent.Slug, detail)
		}
	}
}

func writeLanded(output *strings.Builder, rows []derive.Row) {
	landed := make([]derive.Row, 0)
	for _, row := range rows {
		if row.Status == derive.Landed {
			landed = append(landed, row)
		}
	}
	if len(landed) == 0 {
		return
	}
	sort.Slice(landed, func(i, j int) bool {
		if landingTime(landed[i]) != landingTime(landed[j]) {
			return landingTime(landed[i]) > landingTime(landed[j])
		}
		return landed[i].Intent.Slug > landed[j].Intent.Slug
	})
	fmt.Fprintf(output, "Landed (%d):\n", len(landed))
	count := min(len(landed), 5)
	for _, row := range landed[:count] {
		fmt.Fprintf(output, "  %s  %s\n", row.Intent.Slug, shortID(landingID(row)))
	}
	if len(landed) > 5 {
		fmt.Fprintf(output, "  and %d earlier\n", len(landed)-5)
	}
}

func renderAgents(output *strings.Builder, agents []report.Agent) {
	if len(agents) == 0 {
		return
	}
	output.WriteString("Agents:\n")
	for _, agent := range sortedAgents(agents) {
		fmt.Fprintf(output, "  %s %s Build=%s %s elapsed_ms=%d %s\n    events: %s\n", agent.ID, agent.Role, agent.Build, agent.Status, agent.ElapsedMS, oneLine(agent.Activity), agent.Events)
	}
}

func appendBuildDetails(output *strings.Builder, events []report.Event) {
	modelTimes := make([]string, 0)
	for _, event := range events {
		if eventName(event) == "model_stage" {
			stage := stringField(event, "stage")
			if stage == "" {
				stage = "unknown"
			}
			modelTimes = append(modelTimes, fmt.Sprintf("%s %s", stage, duration(integerField(event, "wall_ms"))))
		}
	}
	if len(modelTimes) > 0 {
		fmt.Fprintf(output, "  model time: %s\n", strings.Join(modelTimes, ", "))
	}
	if setup := firstNamed(events, "setup_reused"); setup != nil {
		fmt.Fprintf(output, "  setup: reused (saved preparation %d ms)\n", integerField(setup, "saved_wall_ms"))
	} else if setup := firstNamed(events, "setup_prepared", "setup_finished"); setup != nil {
		fmt.Fprintf(output, "  setup: prepared in %d ms\n", integerField(setup, "wall_ms"))
	}
	continuations := 0
	for _, event := range events {
		if eventName(event) == "context_continuation" || eventName(event) == "context_continued" {
			continuations++
		}
	}
	if continuations > 0 {
		fmt.Fprintf(output, "  context continuations: %d (same approved Build; checkpoints in journal)\n", continuations)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if eventName(events[i]) == "phase_timing" && stringField(events[i], "phase") == "gate" {
			fmt.Fprintf(output, "  gate: %s\n", duration(integerField(events[i], "wall_ms")))
			break
		}
	}
	for i := len(events) - 1; i >= 0; i-- {
		if oneOf(eventName(events[i]), "check_proposal", "candidate_checks", "check_proposal_created") {
			paths := stringArrayField(events[i], "paths")
			if len(paths) > 0 {
				fmt.Fprintf(output, "  candidate checks (caller approval required): %s\n", strings.Join(paths, ", "))
			}
			break
		}
	}
	verification := lastNamed(events, "verification")
	acceptance, ok := anySliceField(verification, "acceptance")
	if !ok {
		return
	}
	verified := make([]string, 0)
	remaining := make([]string, 0)
	for _, raw := range acceptance {
		id := stringField(raw, "id")
		if id == "" {
			continue
		}
		if oneOf(stringField(raw, "status"), "pass", "passed") {
			verified = append(verified, id)
		} else {
			remaining = append(remaining, id)
		}
	}
	if len(verified) > 0 {
		fmt.Fprintf(output, "  acceptance verified: %s\n", strings.Join(verified, ", "))
	}
	if len(verified) > 0 || len(remaining) > 0 {
		if len(remaining) == 0 {
			remaining = append(remaining, "-")
		}
		fmt.Fprintf(output, "  acceptance remaining: %s\n", strings.Join(remaining, ", "))
	}
}

func buildingDetail(row derive.Row, status report.Report) string {
	if row.Run == nil {
		return "starting"
	}
	stage := "starting"
	for _, event := range status.Runs[row.Run.ID].Events {
		switch eventName(event) {
		case "model_stage":
			if candidate := stringField(event, "stage"); candidate != "" {
				stage = candidate
			}
		case "rung_started":
			if candidate := stringField(event, "rung"); candidate != "" {
				stage = candidate
			}
		}
	}
	return fmt.Sprintf("%s, %s (Build %s)", stage, duration(max(0, status.NowMS-row.Run.StartedAt)), shortID(row.Run.ID))
}

func reason(row derive.Row) string {
	if row.Run != nil && row.Run.Reason != "" {
		return row.Run.Reason
	}
	return "unknown"
}

func runID(row derive.Row) string {
	if row.Run == nil {
		return ""
	}
	return row.Run.ID
}

func landingID(row derive.Row) string {
	if row.Landing == nil {
		return ""
	}
	return row.Landing.Commit
}

func runPath(status report.Report, id string) string {
	return filepath.Join(status.StateRoot, "runs", id)
}

func marshal(value any) ([]byte, error) {
	var output strings.Builder
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(output.String(), "\n")), nil
}

func sortedRows(rows []derive.Row) []derive.Row {
	result := append([]derive.Row(nil), rows...)
	sort.Slice(result, func(i, j int) bool { return result[i].Intent.Slug < result[j].Intent.Slug })
	return result
}

func sortedAgents(agents []report.Agent) []report.Agent {
	result := append([]report.Agent(nil), agents...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Build != result[j].Build {
			return result[i].Build < result[j].Build
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func findRow(rows []derive.Row, slug string) (derive.Row, bool) {
	for _, row := range rows {
		if row.Intent.Slug == slug {
			return row, true
		}
	}
	return derive.Row{}, false
}

func shortID(value string) string {
	if len(value) >= 8 {
		return value[:8]
	}
	return value
}

func duration(milliseconds int64) string {
	seconds := milliseconds / 1000
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%dh%02dm", seconds/3600, (seconds%3600)/60)
}

func oneLine(value string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(strings.TrimSpace(value))
}

func eventName(event report.Event) string { return stringField(event, "event") }

func firstNamed(events []report.Event, names ...string) report.Event {
	for _, event := range events {
		if oneOf(eventName(event), names...) {
			return event
		}
	}
	return nil
}

func lastNamed(events []report.Event, name string) report.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if eventName(events[i]) == name {
			return events[i]
		}
	}
	return nil
}

func stringField(event any, key string) string {
	if fields, ok := event.(map[string]any); ok {
		value, _ := fields[key].(string)
		return value
	}
	if fields, ok := event.(report.Event); ok {
		value, _ := fields[key].(string)
		return value
	}
	return ""
}

func integerField(event report.Event, key string) int64 {
	switch value := event[key].(type) {
	case int:
		return int64(value)
	case int8:
		return int64(value)
	case int16:
		return int64(value)
	case int32:
		return int64(value)
	case int64:
		return value
	case json.Number:
		parsed, err := value.Int64()
		if err == nil {
			return parsed
		}
	case float64:
		if math.Trunc(value) == value && !math.IsNaN(value) && !math.IsInf(value, 0) && value >= math.MinInt64 && value < math.MaxInt64 {
			return int64(value)
		}
	}
	return 0
}

func stringArrayField(event report.Event, key string) []string {
	items, ok := event[key].([]any)
	if !ok {
		if stringsOnly, ok := event[key].([]string); ok {
			return stringsOnly
		}
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func anySliceField(event report.Event, key string) ([]any, bool) {
	items, ok := event[key].([]any)
	return items, ok
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func landingTime(row derive.Row) int64 {
	if row.Landing == nil {
		return 0
	}
	return row.Landing.CommitTime
}
