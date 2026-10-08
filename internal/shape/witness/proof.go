package witness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	"kogen-go/internal/contract"
	"kogen-go/internal/gate"
)

var (
	ErrApplyConflict = errors.New("witness: proof does not apply cleanly to the Build base")
	objectIDPattern  = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	sha256Pattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	slugPattern      = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// Proof is the exact witness object stored in approval.json. DiffSHA256 is
// computed over the byte-for-byte binary diff from BaseSHA to Commit.
type Proof struct {
	Verdict    ProofVerdict `json:"verdict"`
	Commit     string       `json:"commit"`
	DiffSHA256 string       `json:"diff_sha256"`
	BaseSHA    string       `json:"base_sha"`
}

type ProofVerdict string

const (
	VerdictProven             ProofVerdict = "PROVEN"
	VerdictProvenWithConcerns ProofVerdict = "PROVEN_WITH_CONCERNS"
)

func (proof Proof) Validate() error {
	if proof.Verdict != VerdictProven && proof.Verdict != VerdictProvenWithConcerns {
		return errors.New("witness verdict is not proven")
	}
	if !validObjectID(proof.Commit) || !validObjectID(proof.BaseSHA) || !sha256Pattern.MatchString(proof.DiffSHA256) {
		return errors.New("witness proof contains an invalid Git or diff identity")
	}
	return nil
}

// BindingReader must read a direct witness ref and the exact binary Git diff.
// Production adapters should implement both operations through the supervised
// Git port, with no workspace Git config or filters influencing the diff.
type BindingReader interface {
	WitnessRefTarget(context.Context, string) (string, bool, error)
	WitnessDiff(context.Context, string, string) ([]byte, error)
}

// ValidateApproval refuses witness-mode approval unless the proof is valid,
// names the exact approval base, points at refs/kogen/witness/<slug>, and
// hashes the exact diff bytes read from Git.
func ValidateApproval(ctx context.Context, mode, slug, approvalBase string, proof *Proof, reader BindingReader) error {
	if mode != ModeWitness {
		if mode == ModeNone || mode == "" {
			return nil
		}
		return approvalUnproven(errors.New("unknown shaping proof mode"))
	}
	if ctx == nil || !safeSlug(slug) || !validObjectID(approvalBase) || proof == nil || reader == nil {
		return approvalUnproven(errors.New("a bound witness proof is required"))
	}
	if err := proof.Validate(); err != nil || proof.BaseSHA != approvalBase {
		if err == nil {
			err = errors.New("witness proof base does not match the approval base")
		}
		return approvalUnproven(err)
	}
	target, exists, err := reader.WitnessRefTarget(ctx, "refs/kogen/witness/"+slug)
	if err != nil {
		return approvalUnproven(err)
	}
	if !exists || target != proof.Commit {
		return approvalUnproven(errors.New("witness ref does not point at the proof commit"))
	}
	diff, err := reader.WitnessDiff(ctx, proof.BaseSHA, proof.Commit)
	if err != nil {
		return approvalUnproven(err)
	}
	if hashDiff(diff) != proof.DiffSHA256 {
		return approvalUnproven(errors.New("witness diff does not match its approval digest"))
	}
	return nil
}

func approvalUnproven(cause error) error {
	return &contract.Failure{
		Class: "intent", Reason: "unproven", Exit: 1,
		Message: "intent/unproven", Cause: cause,
	}
}

type BuildDecision string

const (
	ContinueLadder BuildDecision = "continue_ladder"
	UseWitness     BuildDecision = "use_witness"
)

// BuildWorkspace is the applied witness candidate on the immutable Build base.
// It remains owned by the caller when Reverify returns UseWitness so the normal
// landing port can publish the verified tree.
type BuildWorkspace any

// ReverifyEffects deliberately has no provider or model method. Apply must
// materialize the proof on TargetBase; Verify must run the real gate. Apply
// returns ErrApplyConflict for an ordinary patch conflict, which falls through
// to the ladder. Verify never receives auditor advice or permission to demote.
type ReverifyEffects interface {
	Apply(context.Context, Proof, string) (BuildWorkspace, error)
	Verify(context.Context, BuildWorkspace) (*gate.GateReport, error)
	Close(context.Context, BuildWorkspace) error
}

type ReverifyRequest struct {
	Mode           string
	TargetBase     string
	TargetBaseTree string
	Proof          *Proof
}

type ReverifyResult struct {
	Decision  BuildDecision
	Workspace BuildWorkspace
	Report    *gate.GateReport
}

// Reverify performs the zero-model B3 check before the caller enters the Build
// ladder. Only a real landable gate report selects UseWitness. A moved-base
// patch conflict or red report selects ContinueLadder without changing any
// acceptance item or invoking an auditor.
func Reverify(ctx context.Context, request ReverifyRequest, effects ReverifyEffects) (ReverifyResult, error) {
	if request.Mode == ModeNone || request.Mode == "" {
		return ReverifyResult{Decision: ContinueLadder}, nil
	}
	if ctx == nil || request.Mode != ModeWitness || !validObjectID(request.TargetBase) ||
		!validObjectID(request.TargetBaseTree) || effects == nil {
		return ReverifyResult{}, ErrInvalidRequest
	}
	if request.Proof == nil || request.Proof.Validate() != nil {
		return ReverifyResult{}, &contract.Failure{
			Class: "controller", Reason: "approval_invalid", Exit: 70,
			Message: "controller/approval_invalid: witness proof is missing or malformed",
		}
	}
	workspace, err := effects.Apply(ctx, *request.Proof, request.TargetBase)
	if errors.Is(err, ErrApplyConflict) {
		if workspace != nil {
			if closeErr := effects.Close(context.WithoutCancel(ctx), workspace); closeErr != nil {
				return ReverifyResult{}, errors.Join(err, fmt.Errorf("close conflicted witness workspace: %w", closeErr))
			}
		}
		return ReverifyResult{Decision: ContinueLadder}, nil
	}
	if err != nil {
		if workspace != nil {
			if closeErr := effects.Close(context.WithoutCancel(ctx), workspace); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close witness workspace: %w", closeErr))
			}
		}
		return ReverifyResult{}, err
	}
	if workspace == nil {
		return ReverifyResult{}, fmt.Errorf("%w: re-verification returned no workspace", ErrInvalidRequest)
	}
	report, verifyErr := effects.Verify(ctx, workspace)
	if verifyErr != nil {
		if closeErr := effects.Close(context.WithoutCancel(ctx), workspace); closeErr != nil {
			verifyErr = errors.Join(verifyErr, fmt.Errorf("close witness workspace: %w", closeErr))
		}
		return ReverifyResult{}, verifyErr
	}
	if report == nil {
		closeErr := effects.Close(context.WithoutCancel(ctx), workspace)
		return ReverifyResult{}, errors.Join(fmt.Errorf("%w: re-verification returned no gate report", ErrInvalidRequest), closeErr)
	}
	receipt, hasReceipt := report.Receipt()
	if !report.IsLandable() || !hasReceipt || receipt.BaseTree != request.TargetBaseTree {
		if closeErr := effects.Close(context.WithoutCancel(ctx), workspace); closeErr != nil {
			return ReverifyResult{}, fmt.Errorf("close red witness workspace: %w", closeErr)
		}
		return ReverifyResult{Decision: ContinueLadder, Report: report}, nil
	}
	return ReverifyResult{Decision: UseWitness, Workspace: workspace, Report: report}, nil
}

func proofFromPublication(slug, baseSHA string, verdict ProofVerdict, publication Publication) (Proof, error) {
	proof := Proof{
		Verdict: verdict, Commit: publication.Commit,
		DiffSHA256: hashDiff(publication.DiffBytes), BaseSHA: publication.BaseSHA,
	}
	if publication.Ref != "refs/kogen/witness/"+slug || publication.BaseSHA != baseSHA {
		return Proof{}, errors.New("witness publication does not match the requested ref and base")
	}
	if err := proof.Validate(); err != nil {
		return Proof{}, err
	}
	return proof, nil
}

func hashDiff(diff []byte) string {
	digest := sha256.Sum256(diff)
	return hex.EncodeToString(digest[:])
}

func validObjectID(value string) bool {
	return objectIDPattern.MatchString(value)
}

func safeSlug(value string) bool {
	return len(value) >= 3 && len(value) <= 48 && slugPattern.MatchString(value)
}
