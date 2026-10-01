package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/secrets"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/sideagent"
)

// sideAgentContextByteLimit — явный верхний предел размера собранного промпта
// одного хода бокового агента (история разговора + текущее сообщение). Нужен,
// чтобы длинный разговор не раздувал промпт бесконтрольно; само сообщение
// отдельно ограничено sideagent.MaxMessageBytes.
const sideAgentContextByteLimit = 512 * 1024

// sideAgentAcctLabel — атрибуция учёта стоимости side-ходов (stage/phase). Боковой
// агент — не стадия флоу, поэтому у него собственный плоский лейбл.
const sideAgentAcctLabel = "side"

// sideAgentEmergencyDrain — ограниченное окно ожидания завершения активного хода
// при аварийном выходе (Ctrl+C / сбой флоу). Не дождались — журнал не закрываем.
const sideAgentEmergencyDrain = 5 * time.Second

// sideAgentRuntime владеет ресурсами бокового агента в пределах executeFlow:
// менеджер (приём ходов + агентские горутины под процессным ctx), журнал
// разговора (chat.jsonl) и учёт стоимости (usage.jsonl в том же namespace).
type sideAgentRuntime struct {
	manager *sideagent.Manager
	store   *sideagent.Store
	acct    *accounting.Store
}

// startSideAgent поднимает боковой агент дашборда. Журнал кладётся под afm-root
// .afm (база fmDir()), keyed по runID — namespace анкорится к afm-root
// независимо от flow.root_dir (то же правило, что у AFM_STAGE_DIR). Боковой агент
// включён всегда, когда поднят дашборд (отдельного конфиг-флага нет).
//
// Жёсткий сбой открытия журнала (store == nil) возвращается ошибкой — вызывающий
// решает, продолжать ли ран без бокового агента (он не должен ронять флоу).
// Повреждённый журнал (store != nil вместе с ErrStorageUnavailable) — не ошибка
// здесь: менеджер сам заблокирует новую работу через store.Unavailable().
func startSideAgent(runID string, cfg config.Config, env runEnvironment) (*sideAgentRuntime, error) {
	store, err := sideagent.Open(fmDir(), runID)
	if err != nil && store == nil {
		return nil, err
	}

	// Учёт side-ходов пишется в usage.jsonl ТОГО ЖЕ namespace, что и журнал
	// (store.Dir() → <dir>/usage.jsonl == store.UsagePath()). Переиспользуем тот
	// же accounting.Store, что и флоу, вместо отдельной машинерии.
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
		Context:     sideagent.ContextBuilder{ByteLimit: sideAgentContextByteLimit},
		Secrets:     func() []string { return protected },
		OnUsage:     onUsage,
	})
	return &sideAgentRuntime{manager: mgr, store: store, acct: acct}, nil
}

// service возвращает значение для server.Config.SideAgent. nil-получатель →
// nil-интерфейс (capability off): боковой агент не поднялся, маршруты
// /api/side-agent* не регистрируются.
func (r *sideAgentRuntime) service() server.SideAgentService {
	if r == nil {
		return nil
	}
	return r.manager
}

// shutdownNormal — штатное завершение (флоу закончился/упал, не Ctrl+C): ход НЕ
// прерывается принудительно, ждём его естественного завершения БЕЗ верхней
// границы (только Ctrl+C через ctx прерывает ожидание), чтобы не оборвать ход
// пользователя 2-минутным UI-grace-колпаком. Приём закрываем ПЕРВЫМ
// (CloseAdmission), ДО Drain: иначе между «Drain увидел, что активного хода нет»
// и выключением сервера мог бы проскочить принятый 202 с новой горутиной, не
// учтённой Drain, и closeStores закрыл бы журнал под ещё живым агентом.
// Возвращает drained.
func (r *sideAgentRuntime) shutdownNormal(ctx context.Context) bool {
	if r == nil {
		return false
	}
	r.manager.CloseAdmission()
	return r.manager.Drain(ctx)
}

// shutdownAfterFlow — штатное завершение с эскалацией. Обычно это shutdownNormal
// (ждём активный ход без 2-минутного колпака). Но если ctx отменён (Ctrl+C пришёл
// во время dashboard-drain или самого ожидания), Drain(ctx) возвращается сразу с
// drained=false, НЕ прерывая подпроцесс — тогда эскалируем к shutdownEmergency со
// свежим ограниченным контекстом (Shutdown → interrupt), иначе боковой агент
// пережил бы возврат executeFlow. Возвращает финальный drained.
func (r *sideAgentRuntime) shutdownAfterFlow(ctx context.Context, emergencyBound time.Duration) bool {
	drained := r.shutdownNormal(ctx)
	if !drained && ctx.Err() != nil {
		drained = r.shutdownEmergency(emergencyBound)
	}
	return drained
}

// shutdownEmergency — аварийное завершение (Ctrl+C / сбой): закрываем приём и
// мягко прерываем активный ход (Shutdown → SIGINT), ждём лишь ограниченное время.
// Не дождались — журнал/учёт НЕ закрываем (см. closeStores): процесс всё равно
// завершается, лучше утечка дескриптора, чем запись в закрытый файл из ещё живой
// агентской горутины. Возвращает drained.
func (r *sideAgentRuntime) shutdownEmergency(bound time.Duration) bool {
	if r == nil {
		return false
	}
	r.manager.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	return r.manager.Drain(ctx)
}

// closeStores закрывает журнал разговора и учёт — ТОЛЬКО если агент действительно
// дренирован (drained). Иначе пропускаем: агентская горутина может быть ещё жива
// и писать в журнал. Вызывается после остановки HTTP-сервера (SSE-подписки уже
// отцеплены), до освобождения wrappers и run-lock.
func (r *sideAgentRuntime) closeStores(drained bool) {
	if r == nil || !drained {
		return
	}
	_ = r.store.Close()
	if r.acct != nil {
		_ = r.acct.Close()
	}
}

// sideAgentProtectedValues собирает ЗНАЧЕНИЯ секретов, подлежащих редакции из
// пользовательского текста бокового агента (инвариант 8). Боковой агент исполняет
// ПРОИЗВОЛЬНУЮ `cfg.Client.Command` с унаследованным окружением, поэтому набор
// должен покрывать не только claude. Резолвится один раз на сборке менеджера
// (окружение и конфиг рана статичны в пределах процесса). Набор:
//   - cross-agent transport-секреты (recipe/hook/sysprompt: AFM_SECRET_*,
//     AFM_HOOK_SECRET_*, AFM_SYSPROMPT_*) — через executor.IsCrossAgentTransportSecret;
//   - провайдеро-нативные credential env поддерживаемых агентов: claude
//     (ClaudeAuthEnvVars), openai/openai-agent (OPENAI_API_KEY), cursor
//     (CURSOR_API_KEY). Без них на нативном хосте (где transport-секретов нет)
//     редакция была бы no-op, а живой токен утёк бы в durable chat.jsonl и UI;
//   - для выбранной команды с docker-рецептом (cfg.Docker.Agents[command]) —
//     резолвленный токен из auth.from (env:/file:) и значение auth.to env.
//     ANTHROPIC_BASE_URL НЕ входит — это URL, а не секрет.
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

// isProviderCredentialEnv — имена env-переменных, через которые поддерживаемые
// провайдеры принимают токен: claude (ClaudeAuthEnvVars) + openai/cursor. Codex
// авторизуется через OAuth (~/.codex), токена в env нет. ANTHROPIC_BASE_URL — URL.
func isProviderCredentialEnv(name string) bool {
	switch name {
	case "OPENAI_API_KEY", "CURSOR_API_KEY":
		return true
	}
	for _, v := range config.ClaudeAuthEnvVars {
		if name == v {
			return true
		}
	}
	return false
}
