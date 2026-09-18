package lifecyclehooks

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// FlushTimeout — bounded-ожидание опустошения очередей при завершении рана.
const FlushTimeout = 10 * time.Second

// hookQueueSize — буфер очереди одного хука. Живой хук, не успевающий за
// потоком событий, начинает терять доставки (best-effort контракт Phase 1).
const hookQueueSize = 256

// DispatcherOptions собирают dispatcher. RunFunc/OnError — seams: тесты
// подменяют RunFunc фейком; OnError получает финальные сбои доставки и
// переполнения очереди (для dashboard-warning).
type DispatcherOptions struct {
	Config  DispatcherConfig
	Hooks   []RegisteredHook
	LogDir  string
	RunFunc func(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error
	OnError func(hookID, eventID string, err error)
}

type delivery struct {
	eventID string
	payload Payload
}

// Dispatcher маршрутизирует события хукам: по одному воркеру на хук (строго
// последовательные доставки для одного потребителя), параллельно между
// разными хуками. Собственный контекст, независимый от run-ctx: хуки —
// наблюдатели, событие flow_interrupted должно уйти даже после отмены рана.
type Dispatcher struct {
	cfg     DispatcherConfig
	hooks   []RegisteredHook
	logDir  string
	runFn   func(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error
	onError func(hookID, eventID string, err error)

	ctx    context.Context
	cancel context.CancelFunc
	queues []chan delivery
	wg     sync.WaitGroup
	sent   atomic.Int64
	done   atomic.Int64

	// mu/stopped закрывают гонку Emit-vs-Stop (codex CRIT#2): Emit шлёт под
	// RLock, Stop закрывает каналы под Lock — пересечение невозможно; Emit
	// после Stop — тихий no-op. Поздние эмиттеры (question poller, agent
	// goroutine, пережившие возврат Run) не паникуют на closed channel.
	mu      sync.RWMutex
	stopped bool
}

// New создаёт dispatcher (воркеры ещё не запущены — см. Start).
func New(opts DispatcherOptions) *Dispatcher {
	runFn := opts.RunFunc
	if runFn == nil {
		runFn = runCommand
	}
	return &Dispatcher{
		cfg:     opts.Config,
		hooks:   opts.Hooks,
		logDir:  opts.LogDir,
		runFn:   runFn,
		onError: opts.OnError,
	}
}

// Start запускает воркеров. Вызывается один раз.
func (d *Dispatcher) Start() {
	d.ctx, d.cancel = context.WithCancel(context.Background())
	d.queues = make([]chan delivery, len(d.hooks))
	for i, rh := range d.hooks {
		d.queues[i] = make(chan delivery, hookQueueSize)
		d.wg.Add(1)
		go d.worker(d.ctx, rh, d.queues[i])
	}
}

// Emit рассылает событие подходящим хукам. Неблокирующий: полная очередь
// одного хука не задерживает эмиттера (оркестратор) — доставка дропается
// с отчётом в OnError.
func (d *Dispatcher) Emit(ev Event) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.stopped || len(d.hooks) == 0 {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	eventID := d.eventID(ev)
	payload := BuildPayload(d.cfg, ev, eventID)
	for i, rh := range d.hooks {
		if !rh.MatchesEvent(ev) {
			continue
		}
		dl := delivery{eventID: eventID, payload: payload}
		select {
		case d.queues[i] <- dl:
			d.sent.Add(1)
		default:
			d.reportError(rh.Hook.ID, eventID, fmt.Errorf("hook queue overflow (%d pending), delivery dropped", hookQueueSize))
		}
	}
}

// eventID — стабильный идентификатор по спеке: FSM-переходы несут durable
// seq, flow-события уникальны в пределах рана, прочие stage-события —
// best-effort (повтор при retry стадии допустим). Для не-FSM stage-событий с
// непустым ev.Key (например, два разных auto-answered вопроса q1/q2 в одной
// стадии/фазе) в id подмешивается Key — иначе разные вопросы схлопнулись бы
// в один и тот же event_id и получатель с идемпотентностью по нему потерял
// бы второе событие. Script-события Key не задают — их повторяемый между
// retry id остаётся как раньше.
func (d *Dispatcher) eventID(ev Event) string {
	switch {
	case ev.Seq > 0:
		return fmt.Sprintf("%s:transition:%d:%s", d.cfg.RunID, ev.Seq, ev.Type)
	case ev.StageID == "":
		return fmt.Sprintf("%s:flow:%s", d.cfg.RunID, ev.Type)
	case ev.Key != "":
		return fmt.Sprintf("%s:%s:%s:%s", d.cfg.RunID, ev.StageID, ev.Key, ev.Type)
	default:
		return fmt.Sprintf("%s:%s:%s", d.cfg.RunID, ev.StageID, ev.Type)
	}
}

func (d *Dispatcher) worker(ctx context.Context, rh RegisteredHook, q <-chan delivery) {
	defer d.wg.Done()
	for dl := range q {
		logPath := filepath.Join(d.logDir, rh.Hook.ID+".log")
		if err := d.runFn(ctx, rh.Hook, d.cfg, dl.payload, logPath); err != nil && ctx.Err() == nil {
			d.reportError(rh.Hook.ID, dl.eventID, err)
		}
		d.done.Add(1)
	}
}

// Flush ждёт, пока все поставленные в очередь доставки завершатся, не дольше
// timeout. true — очереди пусты.
func (d *Dispatcher) Flush(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for d.sent.Load() != d.done.Load() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// Stop закрывает очереди и воркеров; идемпотентен (повторный вызов — no-op).
func (d *Dispatcher) Stop() {
	d.mu.Lock()
	if d.stopped || d.cancel == nil {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	for _, q := range d.queues {
		close(q)
	}
	cancel := d.cancel
	d.mu.Unlock()
	cancel()
	d.wg.Wait()
}

func (d *Dispatcher) reportError(hookID, eventID string, err error) {
	if d.onError == nil {
		return
	}
	d.onError(hookID, eventID, err)
}
