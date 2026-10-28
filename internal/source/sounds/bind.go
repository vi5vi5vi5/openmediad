package sounds

import (
	"errors"
	"sync"
	"time"

	"openmediad/internal/audio"
	"openmediad/internal/engine"
	"openmediad/internal/quasar/mediad"
)

// Привязка к эмуляции mediad: Backend реализует mediad.FileBackend — звонок или системный звук (старт плеера вида 4 с путём
// к файлу) становится потоком движка класса alert. Звуков может быть несколько одновременно (таймер, будильник, щелчок
// громкости): у каждого свой вывод, смешивает их смеситель колонки.

// FileEvents — куда Backend сообщает о результатах (это mediad.Machine).
type FileEvents interface {
	FilePlaying(id string, durMs, bitrate uint64)
	FileEnded(id string) // одноразовый звук доиграл до конца
	FileFailed(id string, err error)
}

// Backend — звонки поверх движка.
type Backend struct {
	Engine *engine.Engine
	Events FileEvents
	Lib    *Library // раскодированные звуки; nil — создаётся сам

	mu     sync.Mutex
	rings  map[string]*ring
	closed bool
}

type ring struct {
	st   *engine.Stream
	info audio.Info
	loop bool
}

var _ mediad.FileBackend = (*Backend)(nil)

func (b *Backend) lib() *Library {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Lib == nil {
		b.Lib = &Library{}
	}
	return b.Lib
}

// Preload раскодирует файл заранее (звонок таймера — при запуске демона).
func (b *Backend) Preload(path string) error { return b.lib().Preload(path) }

func (b *Backend) get(id string) *ring {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rings[id]
}

// forget убирает звонок r из списка (если это всё ещё он).
func (b *Backend) forget(id string, r *ring) {
	b.mu.Lock()
	if b.rings[id] == r {
		delete(b.rings, id)
	}
	b.mu.Unlock()
}

// Start начинает звук: вывод поднимается параллельно с раскодированием, потом звук идёт по кругу до Stop (loop) или один
// раз, и тогда его конец сообщает FileEnded. Путь должен быть из ValidPath (это проверяет автомат mediad).
func (b *Backend) Start(id, path string, loop bool) {
	lib := b.lib()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	r := &ring{loop: loop}
	src := NewSource(lib, path)
	src.Once = !loop
	r.st = b.Engine.NewStream(id, engine.Alert, src, engine.Options{Spectrum: engine.NoSpectrum, Endless: loop}, engine.Events{
		Ready: func(i audio.Info) {
			b.mu.Lock()
			r.info = i
			b.mu.Unlock()
		},
		Playing: func() {
			b.mu.Lock()
			i := r.info
			b.mu.Unlock()
			b.Events.FilePlaying(id, uint64(i.Duration/time.Millisecond), uint64(i.Bitrate))
		},
		Failed: func(err error) {
			b.forget(id, r)
			b.Events.FileFailed(id, err)
		},
		Ended: func() {
			if !loop { // одноразовый звук доиграл; поток остаётся до Stop, чтобы Elapsed показывал, сколько он звучал
				b.Events.FileEnded(id)
				return
			}
			b.forget(id, r) // звонок оборвался, когда уже шёл (вывод умер): автомат закончит его
			b.Events.FileFailed(id, errors.New("звук звонка оборвался"))
		},
	})
	if b.rings == nil {
		b.rings = map[string]*ring{}
	}
	old := b.rings[id]
	b.rings[id] = r
	b.mu.Unlock()
	if old != nil {
		old.st.Close()
	}
	r.st.Play()
}

// Stop обрывает звонок сразу. Остановка aplay идёт в фоне: разбор команды шины её не ждёт.
func (b *Backend) Stop(id string) {
	b.mu.Lock()
	r := b.rings[id]
	delete(b.rings, id)
	b.mu.Unlock()
	if r != nil {
		r.st.Close()
	}
}

// Elapsed — сколько звонок звучит, мс (по часам, не по записанным байтам).
func (b *Backend) Elapsed(id string) uint64 {
	if r := b.get(id); r != nil {
		return uint64(r.st.Elapsed() / time.Millisecond)
	}
	return 0
}

// Pos — позиция внутри текущего прохода файла, мс (у звонка после конца файла отсчёт идёт заново).
func (b *Backend) Pos(id string) uint64 {
	r := b.get(id)
	if r == nil {
		return 0
	}
	b.mu.Lock()
	dur := r.info.Duration
	b.mu.Unlock()
	if !r.loop {
		return uint64(r.st.Elapsed() / time.Millisecond) // движок не пускает позицию дальше длительности
	}
	if dur <= 0 {
		return 0
	}
	return uint64(r.st.Elapsed() % dur / time.Millisecond)
}

// Shutdown обрывает все звонки и больше не принимает новые. Возвращается, когда все выводы остановлены.
func (b *Backend) Shutdown() {
	b.mu.Lock()
	b.closed = true
	all := b.rings
	b.rings = nil
	b.mu.Unlock()
	for _, r := range all {
		r.st.CloseWait()
	}
}
