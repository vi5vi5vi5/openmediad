package spotifysoloist

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cp15Param — параметр ядра arm64 для 32-битных программ: что делать с устаревшими барьерами памяти ARMv6
// (mcr p15, 0, rX, c7, c10, 5). 0 — SIGILL, 1 — эмулировать через ловушку (по умолчанию), 2 — исполнять в железе.
//
// Soloist собран с такими барьерами внутри циклов ldrex/strex. При эмуляции каждая ловушка сбрасывает монитор эксклюзивного
// доступа, strex никогда не удаётся, и главный поток Soloist навсегда застревает в цикле на 100% ядра (проверено на колонке
// 31.10.2026, docs/MUSIC-PLAN.md §10.2). Режим 2 это лечит и для остальных 32-битных программ безвреден (быстрее, чем ловушки).
// Значение действует до перезагрузки; компонент выставляет его при каждом запуске. Включение компонента в сборку — согласие
// пользователя на эту настройку (паспорт компонента, tools/build).
const cp15Rel = "proc/sys/abi/cp15_barrier"

// fixCP15 выставляет abi.cp15_barrier = 2. changed=false, если значение уже такое или параметра нет (другое ядро, не колонка).
func fixCP15(root string) (was string, changed bool, err error) {
	path := filepath.Join(root, cp15Rel)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false, nil
	}
	was = strings.TrimSpace(string(b))
	if was == "2" {
		return was, false, nil
	}
	if err := os.WriteFile(path, []byte("2\n"), 0o644); err != nil {
		return was, false, fmt.Errorf("записать %s: %w (нужен root)", path, err)
	}
	if b, err := os.ReadFile(path); err != nil || strings.TrimSpace(string(b)) != "2" {
		return was, false, fmt.Errorf("%s: ядро не приняло значение 2", path)
	}
	return was, true, nil
}

// cp15Value — текущее значение параметра для статуса («?» — параметра нет).
func cp15Value(root string) string {
	b, err := os.ReadFile(filepath.Join(root, cp15Rel))
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(b))
}
