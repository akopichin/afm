package concurrency

import "context"

// Lease владеет не более чем одним слотом командного семафора одновременно.
// Нужен для перехода исполнения автор→верификатор (другая команда): чтобы
// afm никогда не держал два слота сразу (классический AB-BA/self-дедлок),
// слот на старую команду освобождается ДО захвата слота на новую (см.
// SwapTo). Не потокобезопасен сам по себе — как и агентская горутина,
// которой он принадлежит, используется одним владельцем последовательно.
type Lease struct {
	m    *Manager
	cmd  string
	held bool
}

// AcquireLease захватывает слот для cmd (отменяемо через ctx). cmd
// резолвится той же семантикой, что и Stage.Command в semFor: "" → дефолтная
// команда Manager'а, неизвестная команда → общий noop-семафор (без лимита).
func (m *Manager) AcquireLease(ctx context.Context, cmd string) (*Lease, error) {
	if err := m.semForCmd(cmd).acquireCtx(ctx); err != nil {
		return nil, err
	}
	return &Lease{m: m, cmd: cmd, held: true}, nil
}

// SwapTo переводит lease на другую команду. Та же команда — no-op: слот
// остаётся у владельца, повторного acquire/release нет (иначе на
// max_parallel=1 lease мог бы отпустить единственный слот и тут же
// проиграть гонку за него другому ожидающему — self-дедлок на пустом
// месте). Другая команда — сначала освобождается ТЕКУЩИЙ слот, затем
// отменяемо захватывается новый: если во время ожидания нового слота ctx
// отменяется, lease не держит НИЧЕГО (симметрично с "мы уже отпустили
// старый") и возвращается ошибка — вызывающий не должен считать переход
// частично успешным.
func (l *Lease) SwapTo(ctx context.Context, cmd string) error {
	if l.held && l.cmd == cmd {
		return nil
	}
	l.Release()
	if err := l.m.semForCmd(cmd).acquireCtx(ctx); err != nil {
		return err
	}
	l.cmd = cmd
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
