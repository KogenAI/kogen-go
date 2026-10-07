package intentapprove

import (
	"context"
	"fmt"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/gitio"
	"kogen-go/internal/safefs"
)

type racingGit struct {
	base      contract.GitPort
	world     *world
	slug      string
	remaining int
	attempts  int
}

func (g *racingGit) Exec(ctx context.Context, args []string, stdin []byte, policy contract.GitPolicy) (contract.GitResult, error) {
	if isApprovalCAS(args, g.slug) {
		g.attempts++
		if g.remaining > 0 {
			if err := g.advanceRef(ctx, args[2]); err != nil {
				return contract.GitResult{}, err
			}
			g.remaining--
		}
	}
	return g.base.Exec(ctx, args, stdin, policy)
}

func isApprovalCAS(args []string, slug string) bool {
	return len(args) == 5 && args[0] == "update-ref" && args[1] == "--no-deref" && args[2] == fmt.Sprintf(approvalRefFormat, slug)
}

func (g *racingGit) advanceRef(ctx context.Context, name string) error {
	policy := g.world.policy(g.world.origin)
	refs := gitio.NewRefPort(g.base, policy)
	current, err := refs.ReadRef(ctx, name)
	if err != nil {
		return err
	}
	base := current.Target
	if base == "" {
		base, err = refs.ResolveCommit(ctx, "refs/heads/main")
		if err != nil {
			return err
		}
	}
	tree, err := refs.ResolveTree(ctx, string(base))
	if err != nil {
		return err
	}
	parents := []contract.ObjectID(nil)
	if current.Exists {
		parents = []contract.ObjectID{current.Target}
	}
	competitor, err := refs.CommitTree(ctx, contract.CommitTreeRequest{
		Tree: tree, Parents: parents, Message: []byte("external approval ref update\n"),
	})
	if err != nil {
		return err
	}
	result, err := refs.CompareAndSwap(ctx, contract.RefUpdate{Name: name, Expected: current.Target, Next: competitor})
	if err != nil {
		return err
	}
	if !result.Updated {
		return fmt.Errorf("intentapprove: fixture competitor lost its own ref race")
	}
	return nil
}

type lateWriteOpener struct {
	base     contract.RootOpener
	checkout string
	name     string
	bytes    []byte
	fired    bool
}

func (o *lateWriteOpener) OpenRoot(path string) (contract.RootedFS, error) {
	root, err := o.base.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	if path != o.checkout || o.name == "" {
		return root, nil
	}
	return &lateWriteRoot{RootedFS: root, owner: o}, nil
}

type lateWriteRoot struct {
	contract.RootedFS
	owner *lateWriteOpener
	reads int
}

func (r *lateWriteRoot) ReadFile(name string) ([]byte, error) {
	if name == r.owner.name {
		r.reads++
		if r.reads == 2 && !r.owner.fired {
			root, err := safefs.OpenRoot(r.owner.checkout)
			if err != nil {
				return nil, err
			}
			err = root.Publish(name, r.owner.bytes, 0o644, safefs.PublicationReplace)
			_ = root.Close()
			if err != nil {
				return nil, err
			}
			r.owner.fired = true
		}
	}
	return r.RootedFS.ReadFile(name)
}

func raceAttempts(race string) int {
	switch race {
	case "none":
		return 0
	case "once":
		return 1
	case "twice":
		return 2
	default:
		return -1
	}
}

func matchingApprovalRef(text, slug string) bool {
	return strings.Contains(text, fmt.Sprintf(approvalRefFormat, slug))
}
