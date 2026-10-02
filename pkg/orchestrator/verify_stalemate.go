package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// B1 — verify.review_patience (stalemate detection). Когда verify-отклонение —
// N-е ПОДРЯД, оставившее рабочее дерево НЕИЗМЕННЫМ (автор перестал править в
// ответ на ревью), afm ПРИНИМАЕТ стадию как завершённую вместо очередной
// коррекции/провала — «реализатор выигрывает спор». Дефолт review_patience: 0 =
// выключено = ровно сегодняшнее поведение.

const (
	// fingerprintTimeout — верхняя граница на git-вызовы отпечатка (тот же 3s
	// бюджет, что у workspace/diff.go). Долгий git никогда не блокирует цикл.
	fingerprintTimeout = 3 * time.Second
	// maxUntrackedFileBytes — размерный кап на содержимое одного untracked-файла.
	// Превышение → отпечаток fail-safe (ok=false): мы не можем доказать «без
	// изменений», поэтому НЕ засчитываем раунд как неизменный (см. §2 плана).
	maxUntrackedFileBytes = 1 << 20 // 1 MiB
	// maxGitOutputBytes — кап на объём stdout одного git-вызова. git diff --binary
	// эмитит ПОЛНОЕ base85-содержимое изменённых бинарников; без капа большой
	// модифицированный tracked-бинарь буферизовался бы целиком в память (риск OOM,
	// особенно в memory-constrained Docker). Превышение → ok=false (fail-safe, та
	// же консервативная сторона, что и у untracked-капа; ср. pkg/server/workspace/
	// diff.go, где blob'ы тоже не буферизуются без проверки размера).
	maxGitOutputBytes = 8 << 20 // 8 MiB
	// afmDirName — каталог собственного state-tree afm; исключается из отпечатка,
	// т.к. afm переписывает .afm/runs/<id>/… на КАЖДОМ раунде (иначе stalemate не
	// сработал бы никогда). RunDir всегда лежит под .afm (см. AGENTS.md
	// «AFM_STAGE_DIR always anchored to afm-root»), поэтому этого исключения
	// достаточно — отдельный exclude RunDir был бы избыточен (см. Review
	// decisions в dev-plan.md).
	afmDirName = ".afm"
)

// afmExcludePathspec — git magic pathspec, исключающий .afm/ из diff/ls-files.
const afmExcludePathspec = ":(exclude)" + afmDirName

// workingTreeFingerprint возвращает (hash, true), когда dir — внутри git
// work-tree, и ("", false) в любом другом случае (не git / unborn HEAD / любая
// git-ошибка / нечитаемый или слишком большой untracked-файл). Отпечаток
// покрывает:
//  1. HEAD sha (git rev-parse HEAD) — иначе закоммиченное между снимками
//     изменение выглядело бы неизменным;
//  2. staged (index vs HEAD) и unstaged (worktree vs index) дельты РАЗДЕЛЬНО —
//     единый `git diff HEAD` схлопнул бы взаимно компенсирующие staged/unstaged
//     правки и ложно сигналил бы «без изменений»; --binary ловит изменённые
//     бинарные файлы, а не маскирует их строкой «Binary files differ»;
//  3. для каждого untracked, не-игнорируемого пути — путь И хэш содержимого
//     (plan-стадия правит untracked ./dev-plan.md на месте; path-only отпечаток
//     выглядел бы одинаково между раундами).
//
// Fail-safe, НЕ fail-stable: вызывающий код трактует !ok как «не могу доказать
// неизменность» → раунд НЕ засчитывается как неизменный и счётчик сбрасывается
// (см. runWithRetry). Так пропущенный/слишком большой файл не может
// замаскироваться под «без изменений».
func workingTreeFingerprint(dir string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), fingerprintTimeout)
	defer cancel()

	head, ok := gitCapture(ctx, dir, "rev-parse", "HEAD")
	if !ok {
		return "", false
	}
	// --no-ext-diff и --no-textconv нейтрализуют repo-контролируемые diff-драйверы:
	// иначе (а) внешний diff/textconv-процесс мог бы спавниться на каждый изменённый
	// файл — внук, переживающий ctx-kill и держащий stdout-пайп (hang), и (б)
	// textconv прогонял бы содержимое через произвольный фильтр, делая отпечаток
	// недетерминированным и потенциально маскирующим байтовые изменения. Нам нужен
	// СЫРОЙ git-diff для детекта изменений.
	staged, ok := gitCapture(ctx, dir, "diff", "--no-ext-diff", "--no-textconv", "--cached", "--binary", "--", ".", afmExcludePathspec)
	if !ok {
		return "", false
	}
	unstaged, ok := gitCapture(ctx, dir, "diff", "--no-ext-diff", "--no-textconv", "--binary", "--", ".", afmExcludePathspec)
	if !ok {
		return "", false
	}
	untracked, ok := gitCapture(ctx, dir, "ls-files", "--others", "--exclude-standard", "-z", "--", ".", afmExcludePathspec)
	if !ok {
		return "", false
	}

	h := sha256.New()
	writeComponent(h, "head", []byte(head))
	writeComponent(h, "staged", []byte(staged))
	writeComponent(h, "unstaged", []byte(unstaged))

	for _, rel := range strings.Split(untracked, "\x00") {
		if rel == "" {
			continue
		}
		content, rerr := readCappedRegular(filepath.Join(dir, rel), maxUntrackedFileBytes)
		if rerr != nil {
			return "", false
		}
		writeComponent(h, "path", []byte(rel))
		writeComponent(h, "content", content)
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// gitCapture запускает git -C dir <args> с общим таймаутом отпечатка. Возвращает
// (stdout, true) при нулевом exit И выводе в пределах maxGitOutputBytes, иначе
// ("", false) — любая git-ошибка ИЛИ превышение капа трактуется вызывающим кодом
// как «не могу доказать неизменность». Вывод читается ПОТОКОВО через
// io.LimitReader (не cmd.Output(), который буферизовал бы весь diff --binary
// целиком — риск OOM на большом модифицированном бинарнике); при превышении
// контекст отменяется (убивает git), а остаток пайпа сливается в io.Discard
// потоково — память не растёт, а Wait не виснет на незакрытом пайпе.
func gitCapture(parent context.Context, dir string, args ...string) (string, bool) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", false
	}
	if err := cmd.Start(); err != nil {
		return "", false
	}
	// +1 байт сверх капа — чтобы отличить «ровно кап» от «больше капа».
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxGitOutputBytes+1))
	overflow := len(data) > maxGitOutputBytes
	if overflow {
		cancel() // перестаём читать — убиваем git, память не растёт дальше
	}
	_, _ = io.Copy(io.Discard, stdout) // освобождаем пайп, чтобы Wait не завис
	waitErr := cmd.Wait()
	if overflow || readErr != nil || waitErr != nil {
		return "", false
	}
	return string(data), true
}

// writeComponent добавляет в хэш одну компоненту с префиксом длины — так
// склеенные компоненты нельзя спутать между собой (иначе "ab"+"c" и "a"+"bc"
// дали бы один хэш). hash.Hash.Write по контракту никогда не возвращает ошибку.
func writeComponent(h hash.Hash, label string, data []byte) {
	_, _ = fmt.Fprintf(h, "%s:%d:", label, len(data))
	h.Write(data)
}

// readCappedRegular читает содержимое обычного файла, если он не превышает cap;
// иначе — ошибка (вызывающий код → отпечаток ok=false). Симлинки/FIFO/каталоги
// отвергаются (Lstat, IsRegular) — не следуем за симлинками и не блокируемся на
// FIFO.
func readCappedRegular(path string, capBytes int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > int64(capBytes) {
		return nil, errors.New("file exceeds fingerprint size cap")
	}
	return os.ReadFile(path)
}

// stalemateEligible сообщает, может ли verify-отклонение на шаге rejectStep
// (1-based) претендовать на stalemate-приём. Гейт §3.0 плана (CRITICAL):
//   - отклонивший шаг — агентский (codex), НЕ shell: красный shell-шаг (напр.
//     go test) НИКОГДА не должен быть принят по stalemate;
//   - ПОСЛЕ него нет НИ ОДНОГО shell-шага: RunVerification fail-fast, значит все
//     шаги ДО rejectStep прошли, но шаги ПОСЛЕ в этом проходе не запускались —
//     агентский stalemate не вправе принять изменение, пока неотработавший shell
//     (напр. go test на более позднем индексе) мог бы его отклонить.
func stalemateEligible(s flow.Stage, rejectStep int) bool {
	steps := s.Verify.Steps
	if rejectStep < 1 || rejectStep > len(steps) {
		return false
	}
	if steps[rejectStep-1].Kind != flow.VerifyAgent {
		return false
	}
	for _, st := range steps[rejectStep:] {
		if st.Kind == flow.VerifyShell {
			return false
		}
	}
	return true
}

// handleStalemate обрабатывает ОДИН verify-отказ для B1 (review_patience).
// Возвращает done=true, когда runWithRetry должен немедленно вернуться — либо
// стадия принята по stalemate, либо её уже увели (paused/revising) в окне
// отпечатка; при done=false (продолжить обычную коррекцию) возвращает
// обновлённые счётчики newPrev/newCount. Вынесено из runWithRetry, чтобы не
// превышать лимит вложенности и держать политику stalemate в одном месте рядом
// с отпечатком и stalemateEligible.
func (o *Orchestrator) handleStalemate(ctx context.Context, s flow.Stage, phase string, vre *VerifyRejectedError, patience int, dir, prev string, count int, onUserInterrupted func()) (done bool, newPrev string, newCount int) {
	// Не eligible (shell-отклонение или агентское с неотработавшим shell-шагом
	// после него, §3.0) — сбрасываем счётчик: более поздний stalemate не должен
	// унаследовать счёт, накопленный пока shell-шаг был недоказан.
	if !stalemateEligible(s, vre.Step) {
		return false, "", 0
	}
	fp, ok := o.fingerprintFn(dir)
	switch {
	case !ok:
		// Не можем доказать неизменность → раунд НЕ засчитывается (fail-safe).
		return false, "", 0
	case prev == "":
		// Первое измеримое отклонение — базовый снимок.
		return false, fp, 0
	case fp != prev:
		// Дерево изменилось — новый базовый снимок.
		return false, fp, 0
	}
	// fp == prev: автор ничего не менял с прошлого отклонения.
	count++
	if count < patience {
		return false, prev, count
	}
	// Ownership re-check ПОСЛЕ отпечатка (до ~3s) и НЕПОСРЕДСТВЕННО перед publish
	// — Pause/Revise, закоммиченный в окне отпечатка, не затирается.
	if !o.verifyOutcomeStillOwned(s.ID, onUserInterrupted) {
		return true, "", 0
	}
	o.acceptStalemate(ctx, s, phase, vre, count)
	return true, "", 0
}

// acceptStalemate принимает стадию как завершённую по stalemate — ТОТ ЖЕ
// терминальный success-путь, что и чистый pass (см. runWithRetry, ветка
// checkErr==nil): durable-нотис EventAgentCompleted + publishCritical. Verify
// НИКОГДА сам не зовёт EvComplete и не активирует зависимые (инвариант AGENTS.md)
// — это делает общий каскад обработки EventAgentCompleted, как и для обычного
// завершения. Плюс отдельный громкий durable-сигнал, что приём был
// patience-driven, а не чистым прохождением (emitStalemateAccepted).
func (o *Orchestrator) acceptStalemate(ctx context.Context, s flow.Stage, phase string, vre *VerifyRejectedError, rounds int) {
	command := ""
	if vre.Step >= 1 && vre.Step <= len(s.Verify.Steps) {
		command = s.Verify.Steps[vre.Step-1].Command
	}
	o.emitStalemateAccepted(s.ID, vre.ReportID, vre.Step, command, rounds)
	o.completeAgent(ctx, s.ID, phase)
}

// emitStalemateAccepted публикует durable+live verify-result со синтезированным
// afm вердиктом VerdictAcceptedStalemate — ТОТ ЖЕ канал (EventVerifyResult), что
// и любой другой verify-вердикт, чтобы приём дошёл до ленты и per-stage verify-
// сводки (см. latestVerifyByStage / feed-view-model). Вердикт пишется прямо на
// событие (не декодируется из модели), поэтому строгий DecodeModelResult не
// трогается и продолжает отвергать неизвестный вердикт ОТ модели.
func (o *Orchestrator) emitStalemateAccepted(stageID, verID string, step int, command string, rounds int) {
	summary := fmt.Sprintf("accepted after %d unchanged round(s) (reviewer/author stalemate)", rounds)
	data := map[string]any{
		keyVerificationID: verID,
		keyStep:           step,
		keyVerifyKind:     verifyKindAgent,
		keyVerifyCommand:  command,
		keyVerdict:        string(verify.VerdictAcceptedStalemate),
		keyVerifyReason:   summary,
	}
	o.publishNotice(bus.Event{Type: bus.EventVerifyResult, StageID: stageID, Data: data})
}
