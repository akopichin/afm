package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
)

// fileExceeds — true, если файл существует и его размер строго больше maxBytes.
func fileExceeds(path string, maxBytes int) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.Size() > int64(maxBytes)
}

// lineLimitForBytes выводит жёсткий лимит строк для «экстремального» прохода
// компрессора из байтового порога (≈80 байт/строка), пол — 10.
func lineLimitForBytes(maxBytes int) int {
	n := maxBytes / 80
	if n < 10 {
		return 10
	}
	return n
}

// fifoDropOldestBlocks — терминальный fallback: если компрессор-агент не смог
// уложиться в maxBytes, детерминированно удаляем самые СТАРЫЕ (верхние) блоки
// «## …» файла, сохраняя level-1 заголовок (первая строка «# …»), пока размер
// не уложится в порог. НЕ true-LRU (сигнала recency нет) — FIFO по позиции,
// т.к. updater дописывает новое ниже. Атомарно (temp+rename). Никогда не
// удаляет заголовок целиком.
func fifoDropOldestBlocks(path string, maxBytes int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")

	// Заголовок = ведущие строки до первого «## ».
	header := []string{}
	i := 0
	for ; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			break
		}
		header = append(header, lines[i])
	}
	// Блоки: каждый начинается со строки «## » и идёт до следующей «## ».
	type block struct{ lines []string }
	var blocks []block
	var cur *block
	for ; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			blocks = append(blocks, block{})
			cur = &blocks[len(blocks)-1]
		}
		if cur != nil {
			cur.lines = append(cur.lines, lines[i])
		}
	}

	rendered := func() string {
		var b strings.Builder
		b.WriteString(strings.Join(header, "\n"))
		for _, bl := range blocks {
			b.WriteString("\n")
			b.WriteString(strings.Join(bl.lines, "\n"))
		}
		return b.String()
	}

	// Дропаем самые старые блоки, пока не уложимся или блоки не кончатся.
	for len(blocks) > 0 && len(rendered()) > maxBytes {
		blocks = blocks[1:]
	}

	return atomicWriteFile(path, []byte(rendered()))
}

// atomicWriteFile пишет data в path через temp+rename в той же директории.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mem-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
