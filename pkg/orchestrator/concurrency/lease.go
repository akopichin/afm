package concurrency

import "context"

// Lease владеет не более чем одним слотом командного семафора одновременно.
type Lease struct {
	m    *Manager
	cmd  string
	held bool
}

// AcquireLease захватывает слот для cmd. Lease хранит УЖЕ нормализованное имя — см. resolveCmd/SwapTo.
func (m *Manager) AcquireLease(ctx context.Context, cmd string) (*Lease, error) {
	resolved := m.resolveCmd(cmd)
	if err := m.semForCmd(resolved).acquireCtx(ctx); err != nil {
		return nil, err
	}
	return &Lease{m: m, cmd: resolved, held: true}, nil
}

// SwapTo переводит lease на другую команду.
func (l *Lease) SwapTo(ctx context.Context, cmd string) error {
	resolved := l.m.resolveCmd(cmd)
	if l.held && l.cmd == resolved {
		// Fast path тоже проверяет ctx, чтобы отменённый Pause/Revise не запустил verify на прежней команде.
		return ctx.Err()
	}
	l.Release()
	if err := l.m.semForCmd(resolved).acquireCtx(ctx); err != nil {
		return err
	}
	l.cmd = resolved
	l.held = true
	return nil
}

// Release освобождает удерживаемый слот, если он есть (идемпотентно).
func (l *Lease) Release() {
	if !l.held {
		return
	}
	l.m.semForCmd(l.cmd).release()
	l.held = false
}
