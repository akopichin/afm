package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/secrets"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/sideagent"
	"github.com/akopichin/afm/pkg/state"
)

// sideAgentEnabled resolves the effective side-agent toggle:
// flow (if set) > env AFM_SIDEAGENT > config side_agent.enabled > default ON.
func sideAgentEnabled(cfg config.Config, f *flow.Flow) bool {
	if f != nil && f.SideAgent != nil {
		return *f.SideAgent
	}
	return cfg.SideAgent.IsEnabled()
}

// sideAgentContextByteLimit — явный верхний предел размера собранного промпта одного хода бокового агента.
const sideAgentContextByteLimit = 512 * 1024

// sideAgentAcctLabel — атрибуция учёта стоимости side-ходов (stage/phase). Боковой
// агент — не стадия флоу, поэтому у него собственный плоский лейбл.
const sideAgentAcctLabel = "side"

// sideAgentEmergencyDrain — ограниченное окно ожидания завершения активного хода
// при аварийном выходе (Ctrl+C / сбой флоу). Не дождались — журнал не закрываем.
const sideAgentEmergencyDrain = 5 * time.Second

const openAIProviderEnv = "OPENAI_API_KEY"

// sideAgentRuntime владеет ресурсами бокового агента в пределах executeFlow: менеджер, журнал разговора и
// учёт стоимости.
type sideAgentRuntime struct {
	manager *sideagent.Manager
	store   *sideagent.Store
	acct    *accounting.Store
	mesh    *sideAgentMeshBridge
}

// startSideAgent хранит беседу под afm-root независимо от root_dir; сбой открытия отключает боковой агент,
// не роняя флоу. registry — реестр профилей помощников, нужен ТОЛЬКО для KnownActionType (T4.4): nil
// (collaboration выключена) безопасен — assistants.KnownActionType просто не вызывается. orch — общий
// Service (Agent Mesh Phase 6, T6.7): сюда регистрируется собственная mesh-идентичность бокового агента
// ("side:<runID>"), и отсюда же резолвится ростер для @-mentions — одна реализация адресации на всё дерево,
// вместо собственного ad-hoc matching, который раньше жил здесь через registry.Resolve.
func startSideAgent(runID string, cfg config.Config, env runEnvironment, registry *assistants.Registry, orch *orchestrator.Orchestrator) (*sideAgentRuntime, error) {
	store, err := sideagent.Open(fmDir(), runID)
	if err != nil {
		if store != nil {
			_ = store.Close()
		}
		return nil, err
	}

	// T6.7: регистрируем mesh-идентичность бокового агента ДО того, как он сможет что-либо отправить — иначе
	// первое же @-mention или Send от "side:<runID>" адресовало бы ещё не существующего участника. Сбой
	// регистрации отключает боковой агент тем же способом, что и сбой открытия журнала выше (не роняя флоу).
	if _, err := orch.RegisterSideAgent(context.Background(), orchestrator.RegisterSideAgentRequest{
		DisplayName: cfg.Client.Command, Actor: state.Actor{Kind: "system"},
	}); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("register side agent mesh identity: %w", err)
	}

	// Учёт side-ходов пишется в usage.jsonl ТОГО ЖЕ namespace, что и журнал. Переиспользуем тот же
	// accounting.Store, что и флоу, вместо отдельной машинерии.
	acct, acctErr := accounting.Open(store.Dir(), accounting.NewResolver(cfg.Pricing))
	if acctErr != nil {
		fmt.Fprintf(os.Stderr, "warning: side-agent accounting: %v\n", acctErr)
	}
	var onUsage func(accounting.Observation)
	if acct != nil {
		onUsage = func(obs accounting.Observation) {
			if err := acct.Append(obs, sideAgentAcctLabel, sideAgentAcctLabel, ""); err != nil {
				log.Printf("WARN: side-agent accounting: append usage: %v", err)
			}
		}
	}

	// Набор редактируемых секретов резолвится один раз (окружение/конфиг статичны).
	protected := sideAgentProtectedValues(cfg)

	mgr := sideagent.New(sideagent.Config{
		Store:       store,
		Command:     cfg.Client.Command,
		ExtraArgs:   cfg.Client.ExtraArgs,
		Dir:         env.RootDir,
		WrapperDir:  executor.WrapperDirFor(cfg.Client.Command, env.WrapperDir, env.GeneratedAgents),
		IdleTimeout: cfg.Executor.IdleTimeout,
		Reasoning:   cfg.Client.Reasoning,
		Context:     sideagent.ContextBuilder{ByteLimit: sideAgentContextByteLimit, Instruction: sideagent.DefaultInstruction + prompts.SideAgentCommunicationBlock(string(orch.SideAgentParticipantID()))},
		MeshClient:  orch.SideAgentMeshClient,
		SourceReply: orch.SideAgentExplicitReply,
		OnSourceDelivered: func(sourceMessageID string) {
			if err := orch.MarkMessageDelivered(context.Background(), agentcomm.MessageID(sourceMessageID), orch.SideAgentParticipantID()); err != nil {
				log.Printf("WARN: mark side source delivered: %v", err)
			}
		},
		Secrets: func() []string { return protected },
		OnUsage: onUsage,
		// T6.7: the shared agentcomm.ResolveAlias against the run's LIVE roster — the same resolver every
		// other mesh addressing path uses — instead of a second, ad-hoc match (assistants.Registry.Resolve)
		// against the static profile registry alone, which could not see stage agents/helpers/run agents at
		// all.
		MentionResolve: sideAgentMentionResolve(func() []agentcomm.Participant {
			page, err := orch.ListParticipants(context.Background(), orchestrator.ListParticipantsRequest{
				Actor: state.Actor{Kind: "system"},
			})
			if err != nil {
				return nil
			}
			return page.Participants
		}),
		// KnownActionType (T4.4) is gated the same way as MentionResolve above: an action draft proposes
		// creating a helper task, which is meaningless with collaboration off — nil registry means that.
		KnownActionType: func(typ string) bool {
			return registry != nil && assistants.KnownActionType(typ)
		},
	})
	return &sideAgentRuntime{manager: mgr, store: store, acct: acct}, nil
}

// sideAgentMentionResolve builds sideagent.Config.MentionResolve from a roster-fetcher closure — the same
// narrow-function-injection style Secrets/OnUsage already use for pkg/sideagent's Config (T6.7): pkg/sideagent
// itself must not import pkg/agentcomm, so the ONLY alias-resolution implementation in the tree —
// agentcomm.ResolveAlias — is called from here, in cmd/afm, not duplicated inside pkg/sideagent. An unknown OR
// ambiguous alias (agentcomm.ResolveAlias's *AmbiguousAliasError) both resolve to ok=false — mentions stay
// "opt-in, never an error" exactly as pkg/sideagent.extractMentions already documents for a nil resolver;
// there is no field in TurnAcceptedData for a candidate list to go in, so an ambiguous mention is dropped the
// same way an unknown one is, not surfaced as a distinct outcome.
func sideAgentMentionResolve(roster func() []agentcomm.Participant) func(word string) (string, bool) {
	return func(word string) (string, bool) {
		id, err := agentcomm.ResolveAlias(word, roster())
		if err != nil {
			return "", false
		}
		return string(id), true
	}
}

// service возвращает значение для server.Config.SideAgent. nil-получатель → nil-интерфейс (capability
// off): боковой агент не поднялся, маршруты /api/side-agent* не регистрируются.
func (r *sideAgentRuntime) service() server.SideAgentService {
	if r == nil {
		return nil
	}
	return r.manager
}

// drainMesh closes only Mesh input for flow finalization. The manager still
// accepts direct modal turns until the dashboard grace period ends.
func (r *sideAgentRuntime) drainMesh(ctx context.Context) bool {
	if r == nil || r.mesh == nil {
		return true
	}
	r.mesh.CloseInput()
	return r.mesh.Drain(ctx)
}

// shutdownNormal закрывает admission до Drain, исключая новые ходы; ждёт завершения без лимита, пока ctx
// не отменён.
func (r *sideAgentRuntime) shutdownNormal(ctx context.Context) bool {
	if r == nil {
		return false
	}
	if r.mesh != nil {
		if !r.drainMesh(ctx) {
			return false
		}
	}
	r.manager.CloseAdmission()
	return r.manager.Drain(ctx)
}

// shutdownAfterFlow ждёт штатный drain; при Ctrl+C прерывает ход через bounded shutdownEmergency.
func (r *sideAgentRuntime) shutdownAfterFlow(ctx context.Context, emergencyBound time.Duration) bool {
	drained := r.shutdownNormal(ctx)
	if !drained && ctx.Err() != nil {
		drained = r.shutdownEmergency(emergencyBound)
	}
	return drained
}

// shutdownEmergency — аварийное завершение: закрываем приём и мягко прерываем активный ход, ждём лишь
// ограниченное время. Возвращает drained.
func (r *sideAgentRuntime) shutdownEmergency(bound time.Duration) bool {
	if r == nil {
		return false
	}
	if r.mesh != nil {
		r.mesh.CloseInput()
	}
	r.manager.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	if !r.manager.Drain(ctx) {
		if r.mesh != nil {
			r.mesh.Abort()
		}
		return false
	}
	if r.mesh != nil && !r.mesh.Drain(ctx) {
		r.mesh.Abort()
		return false
	}
	return true
}

// closeStores закрывает журнал разговора и учёт — ТОЛЬКО если агент действительно дренирован. Иначе
// пропускаем: агентская горутина может быть ещё жива и писать в журнал.
func (r *sideAgentRuntime) closeStores(drained bool) {
	if r == nil || !drained {
		return
	}
	_ = r.store.Close()
	if r.acct != nil {
		_ = r.acct.Close()
	}
}

// sideAgentProtectedValues собирает transport-, provider- и recipe-секреты для редакции; BASE_URL не
// считается секретом.
func sideAgentProtectedValues(cfg config.Config) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}

	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		name, value := kv[:i], kv[i+1:]
		if executor.IsCrossAgentTransportSecret(kv) || isProviderCredentialEnv(name) {
			add(value)
		}
	}

	// Рецепт выбранной команды: фактический токен (auth.from) и значение auth.to.
	if r, ok := cfg.Docker.Agents[cfg.Client.Command]; ok {
		if r.Auth.From != "" {
			if v, err := secrets.ResolveRef(r.Auth.From, nil); err == nil {
				add(v)
			}
		}
		if name := r.Auth.EnvVarName(); name != "" {
			add(os.Getenv(name))
		}
	}
	return out
}

// isProviderCredentialEnv — имена env-переменных, через которые поддерживаемые провайдеры принимают токен:
// claude + openai/cursor. Codex авторизуется через OAuth (~/.codex), токена в env нет.
func isProviderCredentialEnv(name string) bool {
	switch name {
	case openAIProviderEnv, "CURSOR_API_KEY":
		return true
	}
	for _, v := range config.ClaudeAuthEnvVars {
		if name == v {
			return true
		}
	}
	return false
}
