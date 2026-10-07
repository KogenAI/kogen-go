package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"kogen-go/internal/contract"
)

type processStub struct {
	result contract.ProcessResult
	spec   contract.ProcessSpec
}

func (p *processStub) Run(_ context.Context, spec contract.ProcessSpec) (contract.ProcessResult, error) {
	p.spec = spec
	return p.result, nil
}

type treeStub struct {
	values []string
	index  int
}

func (t *treeStub) Snapshot(context.Context, string) (string, error) {
	if t.index >= len(t.values) {
		return "", os.ErrNotExist
	}
	value := t.values[t.index]
	t.index++
	return value, nil
}

func TestCheckAdapterReportsActualProcessStatusAndTree(t *testing.T) {
	for _, test := range []struct {
		name   string
		result contract.ProcessResult
		trees  []string
		status contract.CheckStatus
	}{
		{name: "green", result: contract.ProcessResult{ExitStatus: intPointer(0)}, trees: []string{"same", "same"}, status: contract.CheckGreen},
		{name: "red", result: contract.ProcessResult{ExitStatus: intPointer(1)}, trees: []string{"same", "same"}, status: contract.CheckRed},
		{name: "unavailable", result: contract.ProcessResult{ExitStatus: intPointer(127), Unavailable: true}, trees: []string{"same", "same"}, status: contract.CheckUnavailable},
		{name: "timeout", result: contract.ProcessResult{ExitStatus: intPointer(124), TimedOut: true}, trees: []string{"same", "same"}, status: contract.CheckTimeout},
		{name: "mutating", result: contract.ProcessResult{ExitStatus: intPointer(0)}, trees: []string{"before", "after"}, status: contract.CheckMutating},
	} {
		t.Run(test.name, func(t *testing.T) {
			runDir := t.TempDir()
			process := &processStub{result: test.result}
			adapter := &Adapter{Processes: process, Trees: &treeStub{values: test.trees}, RunDir: runDir}
			got, err := adapter.Run(context.Background(), filepath.Join(runDir, "work"), contract.CheckSpec{
				Name: "unit", Program: "sh", Args: []string{"check.sh"}, Env: []string{"PATH=/bin"}, Timeout: 3,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != test.status || got.TreeBefore != test.trees[0] || got.TreeAfter != test.trees[1] {
				t.Fatalf("CheckResult = %#v", got)
			}
			if got.TimedOut != test.result.TimedOut || got.Unavailable != test.result.Unavailable && test.status != contract.CheckUnavailable {
				t.Fatalf("process evidence lost: %#v", got)
			}
			if process.spec.Dir != filepath.Join(runDir, "work") || process.spec.Executable != "sh" || !reflect.DeepEqual(process.spec.Args, []string{"check.sh"}) || process.spec.Timeout != 3 {
				t.Fatalf("ProcessSpec = %#v", process.spec)
			}
			if _, err := os.Stat(process.spec.LogPath); !os.IsNotExist(err) {
				t.Fatalf("stub unexpectedly created process log: %v", err)
			}
		})
	}
}

func intPointer(value int) *int { return &value }
