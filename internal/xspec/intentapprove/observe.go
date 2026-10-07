package intentapprove

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/safefs"
)

type approvalPackage struct {
	Ref    string
	Target contract.ObjectID
	Commit contract.ObjectID
	Count  int
	Hash   string
	By     string
	Base   string
	Feas   string
}

func readApprovalPackage(ctx context.Context, w *world, slug string) (*approvalPackage, error) {
	if !safeFixtureSlug(slug) {
		return nil, errors.New("intentapprove: unsafe approval slug")
	}
	ref := fmt.Sprintf(approvalRefFormat, slug)
	policy := w.policy(w.origin)
	refs := gitio.NewRefPort(w.git, policy)
	current, err := refs.ReadRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !current.Exists {
		return nil, nil
	}
	object := string(current.Target) + ":.kogen/intents/" + slug + "/approval.json"
	result, err := w.git.Exec(ctx, []string{"show", object}, nil, policy)
	if err != nil {
		return nil, err
	}
	if result.Process.ExitStatus == nil || *result.Process.ExitStatus != 0 {
		// A competing update can leave a ref without a package. It is an actual
		// dangling ref, not an approval observation.
		return nil, nil
	}
	var document struct {
		ApprovalSHA256 string `json:"approval_sha256"`
		By             string `json:"by"`
		BaseSHA        string `json:"base_sha"`
		Feasibility    string `json:"feasibility"`
	}
	if err := json.Unmarshal(result.Stdout, &document); err != nil || len(document.ApprovalSHA256) != 64 {
		return nil, nil
	}
	log, err := w.git.Exec(ctx, []string{"log", "--format=%H%x00%B%x00", string(current.Target)}, nil, policy)
	if err != nil {
		return nil, err
	}
	if log.Process.ExitStatus == nil || *log.Process.ExitStatus != 0 {
		return nil, fmt.Errorf("intentapprove: read approval history: %s", strings.TrimSpace(string(log.StderrTail)))
	}
	marker := []byte("Kogen-Approval: " + slug)
	rows := bytes.Split(log.Stdout, []byte{0})
	count := 0
	latest := contract.ObjectID("")
	for index := 0; index+1 < len(rows); index += 2 {
		commit := strings.TrimSpace(string(rows[index]))
		message := rows[index+1]
		for _, line := range bytes.Split(message, []byte{'\n'}) {
			if bytes.Equal(line, marker) {
				count++
				if latest == "" {
					latest = contract.ObjectID(commit)
				}
				break
			}
		}
	}
	return &approvalPackage{
		Ref: ref, Target: current.Target, Commit: latest, Count: count,
		Hash: document.ApprovalSHA256, By: document.By, Base: document.BaseSHA,
		Feas: document.Feasibility,
	}, nil
}

func currentSources(ctx context.Context, w *world, slug string) (intentBytes, acceptanceBytes []byte, exists bool, err error) {
	if !safeFixtureSlug(slug) {
		return nil, nil, false, errors.New("intentapprove: unsafe source slug")
	}
	root, err := safefs.OpenRoot(w.checkout)
	if err != nil {
		return nil, nil, false, err
	}
	defer root.Close()
	intentBytes, err = root.ReadFile(fmt.Sprintf(intentPathFormat, slug))
	if err != nil {
		return nil, nil, false, err
	}
	acceptanceBytes, err = root.ReadFile(fmt.Sprintf(acceptancePathFormat, slug))
	if err != nil {
		return intentBytes, nil, false, nil
	}
	return intentBytes, acceptanceBytes, true, nil
}
