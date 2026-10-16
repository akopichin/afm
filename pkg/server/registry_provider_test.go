package server

// T8.3 — dashboard reads stage config through a RuntimeStageProvider (re-derived from the orchestrator's
// runtime registry on every call) instead of a map frozen once at startup.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// versionedStageProvider's StageConfigs() reflects whatever revision is currently stored — exactly the shape
// an orchestrator-registry-backed provider has (its answer can change between two calls, unlike a frozen
// map), without needing a real orchestrator/registry in this package's tests.
type versionedStageProvider struct {
	mu      sync.Mutex
	configs map[string]StageConfig
}

func (p *versionedStageProvider) StageConfigs() map[string]StageConfig {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]StageConfig, len(p.configs))
	for k, v := range p.configs {
		out[k] = v
	}
	return out
}

func (p *versionedStageProvider) set(configs map[string]StageConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.configs = configs
}

// TestStageView_BuiltFromRegistryProvider: /api/status reflects whatever the provider CURRENTLY returns, not
// a value captured once at server construction — proving the dashboard reads a live provider, not a frozen
// map (the pre-T8.3 shape).
func TestStageView_BuiltFromRegistryProvider(t *testing.T) {
	provider := &versionedStageProvider{configs: map[string]StageConfig{testStageID: {Buttons: []string{"v1"}}}}
	srv := newTestServer(t, Config{Stages: provider})

	first := decodeStatus(t, srv)
	if len(first.Stages) != 1 || len(first.Stages[0].Buttons) != 1 || first.Stages[0].Buttons[0] != "v1" {
		t.Fatalf("first read: Stages = %+v, want buttons [v1]", first.Stages)
	}

	// Simulate a registry revision bump (Этап 9 will drive this from a real Replace; here we just mutate the
	// provider's backing data) BETWEEN the two requests — the second /api/status call must see it.
	provider.set(map[string]StageConfig{testStageID: {Buttons: []string{"v2"}}})

	second := decodeStatus(t, srv)
	if len(second.Stages) != 1 || len(second.Stages[0].Buttons) != 1 || second.Stages[0].Buttons[0] != "v2" {
		t.Fatalf("second read: Stages = %+v, want buttons [v2] (provider must be re-read, not cached from construction)", second.Stages)
	}
}

// TestTopoOrder_StableAcrossRevisions: the display-only topoOrder must produce the SAME order for the SAME
// dependency shape regardless of which "registry revision" the StageConfig map came from, and must never
// mutate/reorder the authoritative ids slice it was given (state.RunState.StageOrder stand-in) — only ever
// return a new slice.
func TestTopoOrder_StableAcrossRevisions(t *testing.T) {
	ids := []string{"a", "b", "c"}
	idsCopy := append([]string(nil), ids...)

	revision1 := map[string]StageConfig{
		"a": {},
		"b": {DependsOn: []string{"a"}},
		"c": {DependsOn: []string{"b"}},
	}
	// "revision 2" differs only in fields topoOrder does not consult (Buttons) — the DAG shape (DependsOn) is
	// unchanged, so the display order must be identical.
	revision2 := map[string]StageConfig{
		"a": {Buttons: []string{"extra"}},
		"b": {DependsOn: []string{"a"}, AutoApprove: true},
		"c": {DependsOn: []string{"b"}, Interactive: true},
	}

	order1 := topoOrder(ids, revision1)
	order2 := topoOrder(ids, revision2)
	if len(order1) != len(order2) {
		t.Fatalf("order1 = %v, order2 = %v: different lengths", order1, order2)
	}
	for i := range order1 {
		if order1[i] != order2[i] {
			t.Fatalf("topoOrder not stable across revisions: order1 = %v, order2 = %v", order1, order2)
		}
	}
	// The authoritative ids slice (state.RunState.StageOrder stand-in) must not have been touched.
	for i := range ids {
		if ids[i] != idsCopy[i] {
			t.Fatalf("topoOrder mutated its ids input: got %v, want %v", ids, idsCopy)
		}
	}
}

// TestStatusRead_NeverMixesTwoGraphVersions: a single /api/status response must be built from ONE call to
// the provider — every stage in that one response must carry the SAME "version tag", never a mix of an old
// and a new registry revision, even while a concurrent goroutine keeps bumping the provider's version. This
// pins the "call StageConfigs() exactly once per response" contract documented on RuntimeStageProvider.
func TestStatusRead_NeverMixesTwoGraphVersions(t *testing.T) {
	const stageCount = 12
	ids := make([]string, stageCount)
	for i := range ids {
		ids[i] = fmt.Sprintf("s%02d", i)
	}

	var version atomic.Int64
	version.Store(1)
	provider := versionTaggedProvider{ids: ids, version: &version}

	runDir := t.TempDir()
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	srv := New(Config{
		RunDir:  runDir,
		Store:   store,
		UIBus:   bus.NewUIBus(),
		Actions: fakeStageActions{},
		Stages:  provider,
	})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			version.Add(1)
		}
	})
	defer func() {
		close(stop)
		wg.Wait()
	}()

	for i := 0; i < 200; i++ {
		resp := decodeStatus(t, srv)
		if len(resp.Stages) != stageCount {
			t.Fatalf("round %d: got %d stages, want %d", i, len(resp.Stages), stageCount)
		}
		want := resp.Stages[0].Buttons
		for _, sv := range resp.Stages {
			if len(sv.Buttons) != 1 || len(want) != 1 || sv.Buttons[0] != want[0] {
				t.Fatalf("round %d: mixed versions in one response: %+v", i, resp.Stages)
			}
		}
	}
}

// versionTaggedProvider tags every stage's single Button with the version counter's value AT THE MOMENT
// StageConfigs() is called — if a caller read the provider once per stage instead of once per response, a
// concurrently-incrementing version could leak two different tags into the same response.
type versionTaggedProvider struct {
	ids     []string
	version *atomic.Int64
}

func (p versionTaggedProvider) StageConfigs() map[string]StageConfig {
	tag := fmt.Sprintf("v%d", p.version.Load())
	out := make(map[string]StageConfig, len(p.ids))
	for _, id := range p.ids {
		out[id] = StageConfig{Buttons: []string{tag}}
	}
	return out
}
