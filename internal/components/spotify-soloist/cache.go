package spotifysoloist

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Сторож звукового кэша. Soloist сам держит кэш около своего предела (-z, не меньше 100 МБ): на колонке за вечер он вырос до
// ≈100 МБ и остановился. Но предел нестрогий (spotify/soloist#8), а флеш колонки мал (/data около 150 МБ, и она нужна самой
// колонке), поэтому сторож защищает главное: свободное место на /data. Если его стало меньше keep_free_mb, а также если кэш
// вышел за предел больше чем на 20%, он удаляет самые старые файлы кэша (`*.file`). Для Soloist это промах кэша: трек
// скачается заново.
type cacheGuard struct {
	dir        string // каталог звукового кэша (-C)
	maxMB      int
	keepFreeMB int
	freeKB     func(path string) (int64, error)
	logf       func(string, ...any)

	mu       sync.Mutex
	trims    int
	removedB int64
	sizeMB   int
	freeMB   int
}

type cacheStatus struct {
	SizeMB   int   `json:"size_mb"`
	FreeMB   int   `json:"free_mb"`
	Trims    int   `json:"trims"`
	RemovedB int64 `json:"removed_bytes"`
}

// setLimits меняет пределы на ходу (настройки перечитаны).
func (g *cacheGuard) setLimits(maxMB, keepFreeMB int) {
	g.mu.Lock()
	g.maxMB, g.keepFreeMB = maxMB, keepFreeMB
	g.mu.Unlock()
}

func (g *cacheGuard) status() cacheStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return cacheStatus{SizeMB: g.sizeMB, FreeMB: g.freeMB, Trims: g.trims, RemovedB: g.removedB}
}

type cacheFile struct {
	path string
	mod  time.Time
	size int64
}

func (g *cacheGuard) scan() (files []cacheFile, total int64) {
	filepath.WalkDir(g.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		if strings.HasSuffix(p, ".file") {
			files = append(files, cacheFile{p, info.ModTime(), info.Size()})
		}
		return nil
	})
	return files, total
}

// maxDeletes — сколько файлов удаляется за один проход (защита от лавины при ошибке измерения).
const maxDeletes = 60

// check измеряет и при необходимости чистит. Возвращает число удалённых файлов.
func (g *cacheGuard) check() int {
	files, total := g.scan()
	free := int64(-1)
	if kb, err := g.freeKB(g.dir); err == nil {
		free = kb / 1024
	}
	g.mu.Lock()
	g.sizeMB = int(total >> 20)
	if free >= 0 {
		g.freeMB = int(free)
	}
	g.mu.Unlock()

	g.mu.Lock()
	maxMB, keepFreeMB := g.maxMB, g.keepFreeMB
	g.mu.Unlock()
	over := total > int64(maxMB)*(1<<20)*12/10
	low := free >= 0 && free < int64(keepFreeMB)
	if !over && !low {
		return 0
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	removed, removedBytes := 0, int64(0)
	targetSize := int64(maxMB) * (1 << 20) * 9 / 10
	for _, f := range files {
		if removed >= maxDeletes {
			break
		}
		if !low && total <= targetSize {
			break
		}
		if low && free >= int64(keepFreeMB)+10 && total <= targetSize {
			break
		}
		if err := os.Remove(f.path); err != nil {
			continue
		}
		removed++
		removedBytes += f.size
		total -= f.size
		free += f.size >> 20
	}
	if removed > 0 {
		g.mu.Lock()
		g.trims++
		g.removedB += removedBytes
		g.sizeMB = int(total >> 20)
		g.mu.Unlock()
		g.logf("кэш Spotify почищен: удалено %d файлов (%d МБ), свободно на /data около %d МБ, кэш %d МБ", removed, removedBytes>>20, max(free, 0), total>>20)
	}
	return removed
}

// run проверяет кэш раз в минуту, пока не отменён ctx.
func (g *cacheGuard) run(ctx context.Context) {
	g.check()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.check()
		}
	}
}
