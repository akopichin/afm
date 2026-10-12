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

// B1 — verify.review_patience. Дефолт review_patience: 0 = выключено = ровно сегодняшнее поведение.

const (
	// fingerprintTimeout — верхняя граница на git-вызовы отпечатка (тот же 3s
	// бюджет, что у workspace/diff.go). Долгий git никогда не блокирует цикл.
	fingerprintTimeout = 3 * time.Second
	// maxUntrackedFileBytes — размерный кап на содержимое одного untracked-файла.
	maxUntrackedFileBytes = 1 << 20 // 1 MiB
	// maxGitOutputBytes ограничивает stdout git; превышение возвращает ok=false, не доказывая неизменность
	// дерева.
	maxGitOutputBytes = 8 << 20 // 8 MiB
	// afmDirName — каталог собственного state-tree afm; исключается из отпечатка, т.к. afm переписывает
	// .afm/runs/<id>/… на КАЖДОМ раунде (иначе stalemate не сработал бы никогда).
	afmDirName = ".afm"
)

// afmExcludePathspec — git magic pathspec, исключающий .afm/ из diff/ls-files.
const afmExcludePathspec = ":(exclude)" + afmDirName

// workingTreeFingerprint хэширует HEAD, раздельные staged/unstaged diff и untracked-контент; при любой
// неопределённости возвращает ok=false.
func workingTreeFingerprint(dir string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), fingerprintTimeout)
	defer cancel()

	head, ok := gitCapture(ctx, dir, "rev-parse", "HEAD")
	if !ok {
		return "", false
	}
	// Отключаем ext-diff/textconv: нужны сырые байтовые изменения без произвольных фильтров и потомков,
	// удерживающих pipe.
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

// gitCapture запускает git -C dir <args> с общим таймаутом отпечатка.
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

// writeComponent добавляет в хэш одну компоненту с префиксом длины — так склеенные компоненты нельзя
// спутать между собой. hash.Hash.Write по контракту никогда не возвращает ошибку.
func writeComponent(h hash.Hash, label string, data []byte) {
	_, _ = fmt.Fprintf(h, "%s:%d:", label, len(data))
	h.Write(data)
}

// readCappedRegular читает содержимое обычного файла, если он не превышает cap; иначе — ошибка.
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

// stalemateEligible сообщает, может ли verify-отклонение на шаге rejectStep претендовать на
// stalemate-приём.
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

// handleStalemate обрабатывает ОДИН verify-отказ для B1.
func (o *Orchestrator) handleStalemate(ctx context.Context, s flow.Stage, phase string, vre *VerifyRejectedError, patience int, dir, prev string, count int, onUserInterrupted func()) (done bool, newPrev string, newCount int) {
	// Не eligible — сбрасываем счётчик: более поздний stalemate не должен унаследовать счёт, накопленный пока
	// shell-шаг был недоказан.
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

// acceptStalemate принимает стадию как завершённую по stalemate — ТОТ ЖЕ терминальный success-путь, что и
// чистый pass: durable-нотис EventAgentCompleted + publishCritical.
func (o *Orchestrator) acceptStalemate(ctx context.Context, s flow.Stage, phase string, vre *VerifyRejectedError, rounds int) {
	command := ""
	if vre.Step >= 1 && vre.Step <= len(s.Verify.Steps) {
		command = s.Verify.Steps[vre.Step-1].Command
	}
	o.emitStalemateAccepted(s.ID, vre.ReportID, vre.Step, command, rounds)
	o.completeAgent(ctx, s.ID, phase)
}

// emitStalemateAccepted публикует afm-вердикт live и durable через verify_result; декодер ответов модели
// этот вердикт не принимает.
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
