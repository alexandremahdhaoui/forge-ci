package reconcilecontroller_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/engineadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	uriManager   = "forge://x/cmd/ci-manager-local@v1"
	uriCompute   = "forge://x/cmd/ci-compute-local@v1"
	uriState     = "forge://x/cmd/ci-state-git@v1"
	uriGate      = "forge://x/cmd/ci-gate-manual@v1"
	uriPromotion = "forge://x/cmd/ci-promotion-all@v1"
	uriTrigger   = "forge://x/cmd/ci-trigger-watch@v1"
	uriRelease   = "forge://x/cmd/ci-artifact-release@v1"
)

type call struct {
	URI  string
	Tool string
}

type fakeEngines struct {
	t     *testing.T
	mu    sync.Mutex
	store map[string]string
	calls []call
	live  int
	peak  int

	published  []citypes.ArtifactInput
	runOutputs map[string]citypes.RunOutput
	gateStatus citypes.Status
	promote    *citypes.PromotionOutput
	failOn     map[call]error

	// declared is what every engine answers declare with; realized records
	// the resource ids a manager was handed, and bootstrapped records the
	// flag it was handed them with.
	declared     []citypes.Resource
	realized     []string
	bootstrapped bool

	// reconcileChanged is what every manager answers changed with, which is
	// how a test stands up a run that found drift and corrected it.
	reconcileChanged bool
}

// plain is an ordinary run: it writes, and it rewrites nothing that cannot
// be compared. A case that names Options is a case about one of those flags.
var plain = reconcilecontroller.Options{}

func newFakeEngines(t *testing.T) *fakeEngines {
	return &fakeEngines{
		t:          t,
		store:      map[string]string{},
		runOutputs: map[string]citypes.RunOutput{},
		gateStatus: citypes.StatusPassed,
		failOn:     map[call]error{},
	}
}

func (f *fakeEngines) caller() *engineadaptermock.MockCaller {
	m := engineadaptermock.NewMockCaller(f.t)
	m.EXPECT().
		Call(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(f.dispatch).Maybe()

	return m
}

func (f *fakeEngines) dispatch(_ context.Context, uri, tool string, in, out any) error {
	f.mu.Lock()
	f.calls = append(f.calls, call{uri, tool})
	err, failing := f.failOn[call{uri, tool}]
	f.mu.Unlock()

	if failing {
		return err
	}

	if uri == uriCompute && tool == "run" {
		f.enter()
		defer f.leave()
	}

	switch {
	case tool == "declare":
		f.mu.Lock()
		declared := append([]citypes.Resource{}, f.declared...)
		f.mu.Unlock()

		return assign(out, citypes.DeclareOutput{Resources: declared})
	case tool == "reconcile":
		var input citypes.ReconcileInput
		require.NoError(f.t, remarshal(in, &input))

		f.mu.Lock()
		f.bootstrapped = f.bootstrapped || input.Bootstrap
		f.mu.Unlock()

		owned := make([]citypes.Ownership, 0, len(input.Resources))
		for _, r := range input.Resources {
			owned = append(owned, citypes.Ownership{Resource: r.ID(), Manager: input.Manager})

			f.mu.Lock()
			f.realized = append(f.realized, r.ID())
			f.mu.Unlock()
		}

		return assign(out, citypes.ReconcileOutput{
			Owned: owned, Actions: []string{"reconciled"}, Changed: f.reconcileChanged,
		})
	case uri == uriState && tool == "get":
		var input citypes.StateGetInput
		require.NoError(f.t, remarshal(in, &input))

		f.mu.Lock()
		payload, ok := f.store[input.Kind+"/"+input.Key]
		f.mu.Unlock()

		return assign(out, citypes.StateGetOutput{Found: ok, Payload: payload})
	case uri == uriState && tool == "put":
		var input citypes.StatePutInput
		require.NoError(f.t, remarshal(in, &input))

		f.mu.Lock()
		f.store[input.Kind+"/"+input.Key] = input.Payload
		f.mu.Unlock()

		return nil
	case uri == uriCompute && tool == "run":
		var input citypes.RunInput
		require.NoError(f.t, remarshal(in, &input))

		result, ok := f.runOutputs[input.Stage+"/"+input.Substage]
		if !ok {
			result = citypes.RunOutput{Status: citypes.StatusPassed}
		}

		return assign(out, result)
	case uri == uriRelease && tool == "publish":
		var input citypes.ArtifactInput
		require.NoError(f.t, remarshal(in, &input))

		f.mu.Lock()
		f.published = append(f.published, input)
		f.mu.Unlock()

		return assign(out, citypes.ArtifactOutput{
			Published: true,
			URL:       "https://example.com/releases/" + input.Version,
			Tagged:    []string{"golden-rust"},
		})
	case uri == uriTrigger && tool == "poll":
		var input struct {
			Spec map[string]any `json:"spec"`
		}
		require.NoError(f.t, remarshal(in, &input))

		previous, _ := input.Spec["previous"].(string)

		return assign(out, citypes.TriggerOutput{
			Fingerprint: "fp1",
			Changed:     previous != "fp1",
			Reason:      "the watched repos moved",
		})
	case uri == uriGate && tool == "evaluate":
		return assign(out, citypes.GateResult{Status: f.gateStatus})
	case uri == uriPromotion && tool == "evaluate":
		if f.promote != nil {
			return assign(out, *f.promote)
		}

		var input citypes.PromotionInput
		require.NoError(f.t, remarshal(in, &input))

		for _, r := range input.Runs {
			if r.Status != citypes.StatusPassed {
				return assign(out, citypes.PromotionOutput{Advance: false, Reason: "a substage failed"})
			}

			for _, g := range r.Gates {
				if g.Status != citypes.StatusPassed {
					return assign(out, citypes.PromotionOutput{Advance: false, Reason: "a gate is not satisfied"})
				}
			}
		}

		return assign(out, citypes.PromotionOutput{Advance: true, Reason: "all good"})
	}

	f.t.Fatalf("the fake engines received an unexpected call: %s %s", uri, tool)

	return nil
}

func (f *fakeEngines) enter() {
	f.mu.Lock()
	f.live++

	if f.live > f.peak {
		f.peak = f.live
	}

	f.mu.Unlock()

	time.Sleep(20 * time.Millisecond)
}

func (f *fakeEngines) leave() {
	f.mu.Lock()
	f.live--
	f.mu.Unlock()
}

func (f *fakeEngines) counted(c call) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := 0

	for _, got := range f.calls {
		if got == c {
			n++
		}
	}

	return n
}

func assign(out any, value any) error {
	if out == nil {
		return nil
	}

	return remarshal(value, out)
}

func remarshal(from, into any) error {
	raw, err := json.Marshal(from)
	if err != nil {
		return err
	}

	return json.Unmarshal(raw, into)
}

func clock() func() time.Time {
	var mu sync.Mutex

	n := 0

	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		n++

		return time.Date(2026, 8, 19, 0, 0, n, 0, time.UTC)
	}
}

func pipeline(stages ...config.Stage) config.Pipeline {
	return config.Pipeline{
		Name:  "demo",
		Repos: []config.Repo{{Name: "golden-rust", URL: "git@example.com:golden-rust.git"}},
		Managers: []config.Manager{
			{Alias: "local", Engine: uriManager, Spec: map[string]any{"statePath": "/tmp/m.json"}},
		},
		Engines: []config.Engine{
			{Alias: "here", Type: config.PortCompute, Engine: uriCompute, Manager: "local"},
			{
				Alias: "st", Type: config.PortState, Engine: uriState, Manager: "local",
				Spec: map[string]any{"path": "/tmp/state"},
			},
			{Alias: "approve", Type: config.PortGate, Engine: uriGate, Manager: "local"},
			{Alias: "all-pass", Type: config.PortPromotion, Engine: uriPromotion, Manager: "local"},
			{Alias: "gh", Type: config.PortArtifact, Engine: uriRelease, Manager: "local"},
		},
		State: "st",
		Targets: []config.Target{
			{Alias: "build", Forge: "test-all", In: []string{"golden-rust"}},
			{Alias: "self", ForgeCI: "apply"},
		},
		Stages: stages,
	}
}

// stage mints, because most tests here assert on the recorded revision. A
// stage that must not mint is built with mintlessStage.
func stage(name string, subs ...config.Substage) config.Stage {
	return config.Stage{Name: name, Mint: true, Promotion: "all-pass", Substages: subs}
}

func mintlessStage(name string, subs ...config.Substage) config.Stage {
	return config.Stage{Name: name, Promotion: "all-pass", Substages: subs}
}

func releasingStage(name string, subs ...config.Substage) config.Stage {
	return config.Stage{
		Name: name, Mint: true, Release: "gh", Promotion: "all-pass", Substages: subs,
	}
}

func substage(name string, targets []string, gates ...string) config.Substage {
	return config.Substage{
		Name: name, Engine: "here", Manager: "local", Targets: targets, Gates: gates,
	}
}

func mockAny() any { return mock.Anything }
