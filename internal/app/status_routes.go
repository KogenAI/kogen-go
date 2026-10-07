package app

import (
	"context"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"kogen-go/internal/cli/parse"
	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/intent"
	"kogen-go/internal/project"
	"kogen-go/internal/safefs"
	"kogen-go/internal/status/derive"
	statusrender "kogen-go/internal/status/render"
	"kogen-go/internal/status/report"
)

func (cli *CLI) status(command parse.Command, cwd string) int {
	if command.Watch {
		return cli.writeError("controller", "internal_error", "status watch is closed with queue lifecycle in the Build integration round", 70)
	}
	ctx := context.Background()
	ports := cli.ports()
	resolved, exit, message := cli.resolveProject(ctx, command, cwd, ports)
	if exit != 0 {
		return cli.writeRawError(message, exit)
	}
	status, err := inspectSyntheticStatus(ctx, ports, resolved)
	if err != nil {
		return cli.writeError("environment", "status_unavailable", "could not read project status", 3)
	}
	if command.Slug == nil {
		if command.JSON {
			output, err := statusrender.JSONLines(status)
			if err != nil {
				return cli.writeError("controller", "internal_error", "could not encode status JSON", 70)
			}
			_, _ = ioWriteString(cli.Out, output)
			return 0
		}
		_, _ = ioWriteString(cli.Out, statusrender.Overview(status))
		return 0
	}
	row, ok := findStatusRow(status.Board, *command.Slug)
	if !ok {
		return cli.writeError("intent", "not_found", "Intent does not exist", 2)
	}
	if command.JSON {
		encoded, err := report.JSON(row, status)
		if err != nil {
			return cli.writeError("controller", "internal_error", "could not encode Build status", 70)
		}
		_, _ = ioWriteString(cli.Out, string(encoded)+"\n")
		return 0
	}
	_, _ = ioWriteString(cli.Out, statusrender.Slug(row, status))
	return 0
}

// inspectSyntheticStatus joins the current checkout's Intent sources with
// real approval refs, then delegates classification, queue order and output
// formatting to the shared status components. Build records, agents, recovery
// and live queue ownership are added by the Build integration round.
func inspectSyntheticStatus(ctx context.Context, ports runtimePorts, resolved *project.Resolution) (report.Report, error) {
	if ctx == nil || resolved == nil || ports.originGit == nil {
		return report.Report{}, errors.New("status inspection requires a resolved project")
	}
	root, err := (safefs.Opener{}).OpenRoot(resolved.Checkout)
	if err != nil {
		return report.Report{}, err
	}
	defer closeRootFS(root)
	intents, err := readIntentSources(root)
	if err != nil {
		return report.Report{}, err
	}
	approvals, err := readApprovalRefs(ctx, ports, resolved)
	if err != nil {
		return report.Report{}, err
	}
	landings, err := readCurrentLandings(ctx, ports, resolved, intents)
	if err != nil {
		return report.Report{}, err
	}
	rows := make([]derive.Intent, 0, len(intents))
	for slug, source := range intents {
		row := derive.Intent{Slug: slug}
		if parsed, parseErr := intent.Parse(slug, source); parseErr == nil {
			row.Priority = int(parsed.Frontmatter.Priority)
			row.Dependencies = append([]string(nil), parsed.Frontmatter.BlocksOn...)
		}
		if approval, ok := approvals[slug]; ok {
			row.ApprovalCommit = approval.commit
			row.ApprovalTime = approval.timestamp
		}
		rows = append(rows, row)
	}
	board := derive.Derive(derive.Input{Intents: rows, Landings: landings})
	return report.Report{
		Board: board, NowMS: time.Now().UnixMilli(), StateRoot: resolved.StateRoot,
		Approvals: map[string]any{}, Runs: map[string]report.RunData{},
	}, nil
}

type statusApproval struct {
	commit    string
	timestamp int64
}

func readIntentSources(root contract.RootedFS) (map[string][]byte, error) {
	result := make(map[string][]byte)
	info, err := root.Lstat(".kogen/intents")
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, errors.New("Intent directory is not a real directory")
	}
	entries, err := root.ReadDir(".kogen/intents")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		slug := entry.Name()
		if !entry.IsDir() || !validStatusSlug(slug) {
			continue
		}
		directory := ".kogen/intents/" + slug
		info, err := root.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		file := directory + "/intent.md"
		info, err = root.Lstat(file)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		contents, err := root.ReadFile(file)
		if err == nil {
			result[slug] = contents
		}
	}
	return result, nil
}

func readApprovalRefs(ctx context.Context, ports runtimePorts, resolved *project.Resolution) (map[string]statusApproval, error) {
	policy := gitio.OriginPolicy(resolved.Origin, ports.env)
	result, err := ports.originGit.Exec(ctx, []string{
		"for-each-ref", "--format=%(refname)%00%(objectname)%00%(committerdate:unix)", "refs/kogen/intents/",
	}, nil, policy)
	if err != nil || !success(result.Process) {
		return nil, errors.New("cannot read approval refs")
	}
	approvals := make(map[string]statusApproval)
	for _, line := range strings.Split(strings.TrimSuffix(string(result.Stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 {
			continue
		}
		slug := strings.TrimPrefix(fields[0], "refs/kogen/intents/")
		if !validStatusSlug(slug) || fields[1] == "" {
			continue
		}
		timestamp, _ := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
		approvals[slug] = statusApproval{commit: fields[1], timestamp: timestamp}
	}
	return approvals, nil
}

func readCurrentLandings(ctx context.Context, ports runtimePorts, resolved *project.Resolution, intents map[string][]byte) ([]derive.Landing, error) {
	policy := gitio.OriginPolicy(resolved.Origin, ports.env)
	log, err := ports.originGit.Exec(ctx, []string{
		"log", resolved.Base, "--format=%H%x00%ct%x00%(trailers:key=Kogen-Intent,valueonly)",
	}, nil, policy)
	if err != nil || !success(log.Process) {
		return nil, errors.New("cannot read landing history")
	}
	var landings []derive.Landing
	for _, line := range strings.Split(strings.TrimSuffix(string(log.Stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\x00", 3)
		if len(fields) != 3 {
			continue
		}
		commit, timestampText, trailerValues := fields[0], fields[1], fields[2]
		timestamp, _ := strconv.ParseInt(strings.TrimSpace(timestampText), 10, 64)
		for _, slug := range strings.Fields(trailerValues) {
			current, exists := intents[slug]
			if !exists || !validStatusSlug(slug) {
				continue
			}
			object, err := ports.originGit.Exec(ctx, []string{"show", commit + ":.kogen/intents/" + slug + "/intent.md"}, nil, policy)
			if err != nil || !success(object.Process) || string(object.Stdout) != string(current) {
				continue
			}
			landings = append(landings, derive.Landing{Slug: slug, Commit: commit, CommitTime: timestamp})
		}
	}
	return landings, nil
}

func findStatusRow(board derive.Board, slug string) (derive.Row, bool) {
	for _, row := range board.Rows {
		if row.Intent.Slug == slug {
			return row, true
		}
	}
	return derive.Row{}, false
}

func success(result contract.ProcessResult) bool {
	return result.ExitStatus != nil && *result.ExitStatus == 0 && !result.TimedOut && !result.Unavailable
}

func validStatusSlug(slug string) bool {
	if len(slug) < 3 || len(slug) > 48 {
		return false
	}
	previousDash := true
	for _, character := range slug {
		if character == '-' {
			if previousDash {
				return false
			}
			previousDash = true
			continue
		}
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				return false
			}
		}
		previousDash = false
	}
	return !previousDash
}

func closeRootFS(root contract.RootedFS) {
	if closer, ok := root.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}
