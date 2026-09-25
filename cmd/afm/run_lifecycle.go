package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
)

func combineRunHooks(cfg config.Config, f *flow.Flow) []lifecyclehooks.RegisteredHook {
	layers := []lifecyclehooks.Layer{{Hooks: cfg.Hooks}, {Hooks: f.Hooks}}
	for _, st := range f.Stages {
		if len(st.Hooks) > 0 {
			layers = append(layers, lifecyclehooks.Layer{StageID: st.ID, Hooks: st.Hooks})
		}
	}
	return lifecyclehooks.Combine(layers...)
}

// Resolve secrets before constructing the dispatcher or starting any agents.
// Start is left to the caller so its error handler can reference the orchestrator.
func prepareLifecycleDispatcher(afmRoot string, meta lifecyclehooks.DispatcherConfig, hooks []lifecyclehooks.RegisteredHook, onError func(string, string, error)) (*lifecyclehooks.Dispatcher, error) {
	if err := resolveLifecycleEnv(afmRoot, hooks); err != nil {
		return nil, err
	}
	if len(hooks) == 0 {
		return nil, nil
	}
	return lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
		Config:  meta,
		Hooks:   hooks,
		LogDir:  filepath.Join(meta.RunDir, "hooks"),
		OnError: onError,
	}), nil
}

func resolveLifecycleEnv(afmRoot string, hooks []lifecyclehooks.RegisteredHook) error {
	if config.ReExecedIntoContainer() {
		for i := range hooks {
			resolved, err := lifecyclehooks.ResolveHookEnvFromTransport(i, hooks[i].Hook)
			if err != nil {
				return fmt.Errorf("lifecycle hooks: %w", err)
			}
			hooks[i].Hook.ResolvedEnv = resolved
		}
		// No child process may inherit the transient secret transport.
		lifecyclehooks.UnsetTransportVars()
		return nil
	}

	var secrets map[string]string
	for _, h := range hooks {
		if len(h.Hook.Env) == 0 {
			continue
		}
		var err error
		// Load lazily: hooks without env must work without readable secrets files.
		secrets, err = loadHookSecretLayers(afmRoot)
		if err != nil {
			return fmt.Errorf("lifecycle hooks: load secrets.env: %w", err)
		}
		break
	}
	for i := range hooks {
		if len(hooks[i].Hook.Env) == 0 {
			continue
		}
		resolved, err := lifecyclehooks.ResolveHookEnv(hooks[i].Hook, secrets)
		if err != nil {
			return fmt.Errorf("lifecycle hooks: %w", err)
		}
		hooks[i].Hook.ResolvedEnv = resolved
	}
	return nil
}

func stopLifecycleDispatcher(d *lifecyclehooks.Dispatcher) {
	if d == nil {
		return
	}
	// Flush on errors too, using the dispatcher's independent context so an
	// interrupted run can still deliver its terminal lifecycle event.
	if !d.Flush(lifecyclehooks.FlushTimeout) {
		fmt.Fprint(os.Stderr, "warning: lifecycle hooks flush timed out\n")
	}
	d.Stop()
}
