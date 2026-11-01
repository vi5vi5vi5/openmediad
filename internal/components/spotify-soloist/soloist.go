// Package spotifysoloist — Spotify Connect на колонке через официальный движок Soloist (Spotify, закрытая сборка armv7l).
//
// Как это устроено:
//
//	приложение Spotify ── mDNS/HTTP ──► Soloist (процесс; вход аккаунта, облако Spotify, расшифровка, декодер)
//	                                       │ вызывает функции PulseAudio
//	                                       ▼
//	                              шим libpulse.so.0 (наш, вшит в бинарник)
//	                                       │ unix-сокет, кадры PCM + пауза/flush/громкость
//	                                       ▼
//	                     pcmServer → pcmstream.Source → engine.Stream (класс music) → aplay
//
// Звук проходит через движок openmediad, поэтому работают приглушение под Алису, спектр для дисплея, маршруты [route]
// (наушники) и пауза с возвратом непрозвучавшего звука. Управление остаётся у приложения Spotify; сам компонент следит за
// процессом (перезапуск, зависание, истёкшая сборка), кэшем и уступает звук Яндексу и наоборот.
//
// Что компонент делает с системой колонки: при запуске выставляет abi.cp15_barrier = 2 (см. sysctl.go); пишет только в свой
// каталог на /data и в рабочий каталог демона.
package spotifysoloist

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"openmediad/internal/core/engine"
	"openmediad/internal/core/led"
	"openmediad/internal/core/platform/disk"
	"openmediad/internal/core/platform/layout"
	"openmediad/internal/core/quasar/mediad"
	alicedclient "openmediad/internal/libs/aliced-client"
	"openmediad/internal/libs/volmap"
)

// machine — то, что компонент берёт у автомата mediad: играет ли музыка Яндекса и как забрать у неё место.
type machine interface {
	Playing() bool
	// TakeOver останавливает музыку Яндекса, оставляя платформе её плеер уже под Spotify (virtual.go автомата).
	TakeOver() int
	// ArmCarrier: ближайший запуск музыки Яндекса платформой (после быстрой команды) достаётся Spotify.
	ArmCarrier(d time.Duration)
	// SetVirtual и OnVirtualCommand — Spotify как плеер платформы (virtual.go).
	SetVirtual(v *mediad.Virtual)
	OnVirtualCommand(f func(cmd string))
}

// soloistNice — приоритет Soloist: демон работает с повышенным, а декодеру он ни к чему, голос Алисы важнее.
const soloistNice = 5

// Component — компонент в работающем демоне (module по интерфейсам демона: Start/Stop, StatusLine, DebugState).
type Component struct {
	cfg  Config
	p    paths
	eng  *engine.Engine
	mach machine
	logf func(string, ...any)

	pcm    *pcmServer
	ctl    *control
	sup    *supervisor
	cache  *cacheGuard
	slog   *soloistLog
	vol    *volumeSync // nil, если громкость не связана с колонкой
	master masterVol   // регулятор колонки, открытый при запуске

	cfgMu sync.RWMutex // настройки меняются на ходу (Reload)

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	needs   []string // чего не хватает для запуска
	cp15    string   // что сделано с abi.cp15_barrier
	login   string   // последняя важная строка о входе из журнала Soloist
	expires string   // «client expires in N days» из журнала Soloist
	yields  int      // сколько раз уступали звук другому источнику

	owner        string    // кто последним начал играть: ownerSpotify | ownerYandex (voice.go)
	voiceHandled int       // сколько фраз выполнено вместо Алисы
	voiceLast    string    // последнее выполненное действие
	voiceAt      time.Time // и когда
	voiceAct     action    // что выполнила последняя перехваченная фраза (для защиты от повтора директивой облака)
	platformAt   time.Time // когда платформа последний раз сама адресовала команду плееру Spotify (buttons.go)
	likes        int       // сколько лайков поставлено или снято
	likeHintAt   time.Time // когда в последний раз напоминали в журнале, что для лайков нужен вход
	likeErr      string    // чем кончился последний лайк (пусто — удачно)

	web     *webAPI       // Web API Spotify (лайки, поиск)
	aliases aliasStore    // свои названия: «кино» → ссылка Spotify
	led     led.Indicator // подсветка: Spotify играет (nil — без подсветки)

	reqs         map[string]*pendingReq // просьбы «включи <название>», чьего ответа облака ждём (request.go)
	searchHintAt time.Time
	resolve      func(playRequest) (resolution, error) // подмена поиска в тестах
	swallow      func()                                // погасить ближайший ответ Алисы голосом (служба демона)

	cancelFn func(context.Context) error // отмена диалога Алисы (в тестах подменяется)
}

func newComponent(cfg Config, eng *engine.Engine, m machine, logf func(string, ...any), run string) *Component {
	c := &Component{cfg: cfg, p: newPaths(cfg.Dir, run, cfg.KeyFile), eng: eng, mach: m, logf: logf, cancelFn: defaultCancel}
	c.web = newWebAPI(c.p, func() string { return c.config().ClientID })
	c.web.proxy = func() string { return c.config().WebProxy }
	c.aliases = newAliasStore(c.p)
	return c
}

// config — снимок настроек (они могут поменяться на ходу).
func (c *Component) config() Config {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.cfg
}

// Start запускает всё в фоне; если Soloist не установлен или нет ключа, только сообщает об этом.
func (c *Component) Start(ctx context.Context) error {
	if c.eng == nil {
		return fmt.Errorf("spotify-soloist: нет движка")
	}
	if miss := c.p.missing(); len(miss) > 0 {
		c.mu.Lock()
		c.needs = miss
		c.mu.Unlock()
		c.logf("Spotify не настроен: нет %s. Установка: openmediad spotify setup; ключ: openmediad spotify setkey; потом openmediad restart", strings.Join(miss, ", "))
		return nil
	}
	cfg := c.config()
	c.cp15 = "не менялась (fix_cp15 = false)"
	if cfg.FixCP15 {
		was, changed, err := fixCP15(layout.Root)
		switch {
		case err != nil:
			c.cp15 = "ошибка: " + err.Error()
			c.logf("abi.cp15_barrier не выставлен: %v — Soloist будет зависать", err)
		case changed:
			c.cp15 = fmt.Sprintf("выставлена при запуске (было %s)", was)
			c.logf("изменена системная настройка ядра: abi.cp15_barrier %s → 2 (до перезагрузки; нужна Soloist, иначе он зависает)", was)
		default:
			c.cp15 = "уже 2 или недоступна"
		}
	}
	if killOrphan(c.p.pid(), c.p.soloist()) {
		c.logf("остановлен Soloist, оставшийся от прошлого запуска")
	}

	c.pcm = newPCMServer(c.p.sock(), c.eng, c.logf)
	c.pcm.latency = time.Duration(cfg.LatencyMS) * time.Millisecond
	c.pcm.onEvent = c.onPlayChange
	if err := c.pcm.Start(); err != nil {
		return err
	}
	c.ctl = newControl(fmt.Sprintf("127.0.0.1:%d", cfg.WSPort), c.logf)
	c.slog = &soloistLog{logf: c.logf, onKey: c.onSoloistLine}
	c.sup = &supervisor{start: c.startProc, probe: threadTicks, logf: c.logf, kick: make(chan struct{}, 1)}
	c.cache = &cacheGuard{dir: c.p.cache(), maxMB: cfg.CacheMB, keepFreeMB: cfg.KeepFreeMB, freeKB: disk.FreeKB, logf: c.logf}

	routines := []func(context.Context){c.ctl.run, c.sup.run, c.cache.run, c.arbiter}
	if cfg.VolumeSync {
		if m, err := openMaster(); err != nil {
			c.logf("громкость колонки недоступна (%v): ползунок Spotify не связан с колонкой, громкость регулирует сам Soloist", err)
		} else {
			c.master = m
			lv, err := volmap.Load(volmap.QuasarConfig)
			if err != nil {
				c.logf("таблица громкости платформы не прочитана (%v): беру таблицу bergamot", err)
			}
			ask := &alicedclient.Client{}
			c.vol = newVolumeSync(m, lv, c.ctl, func(n int) error { // как голосом: «громкость N», уровень платформы 2N
				return ask.Ask(context.Background(), fmt.Sprintf("громкость %d", n))
			}, c.logf)
			routines = append(routines, c.vol.run)
		}
	}

	c.ctl.changed = func() { // состояние Soloist изменилось: громкость и плеер платформы
		if c.vol != nil {
			c.vol.poke()
		}
		c.virtualPush()
	}
	if c.mach != nil {
		c.mach.OnVirtualCommand(c.onVirtualCommand)
	}

	ctx, c.cancel = context.WithCancel(ctx)
	for _, f := range routines {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			f(ctx)
		}()
	}
	return nil
}

// Stop останавливает Soloist (SIGTERM, через 5 с SIGKILL) и закрывает потоки звука.
func (c *Component) Stop() {
	if c.cancel == nil {
		return
	}
	c.cancel()
	c.wg.Wait()
	if c.led != nil {
		c.led.Off(led.Source, Name)
	}
	if c.mach != nil {
		c.mach.SetVirtual(nil) // платформа не должна считать, что Spotify ещё играет
	}
	c.pcm.Stop()
	os.Remove(c.p.pid())
	c.cancel = nil
}

// startProc запускает Soloist через загрузчик glibc из каталога установки.
func (c *Component) startProc() (process, error) {
	cfg := c.config()
	key, err := os.ReadFile(c.p.key())
	if err != nil {
		return nil, fmt.Errorf("ключ Soloist: %w", err)
	}
	args := []string{"--library-path", c.p.lib() + ":/lib", c.p.soloist(),
		"-n", cfg.DeviceName, "-k", strings.TrimSpace(string(key)),
		"-D", c.p.data(), "-C", c.p.cache(), "-z", strconv.Itoa(cfg.CacheMB), "-i", strconv.Itoa(c.startVolume(cfg)),
		"-w", fmt.Sprintf("127.0.0.1:%d", cfg.WSPort)}
	if cfg.Verbose {
		args = append(args, "-v")
	}
	shimVolume := "linear"
	if c.vol != nil {
		shimVolume = "off" // громкость ведёт колонка (Master), шим её не применяет: иначе она бы перемножалась
	}
	cmd := exec.Command(c.p.loader(), args...)
	cmd.Dir = c.p.dir
	cmd.Env = append(os.Environ(),
		"HOME="+c.p.dir, // без записи в /root: корень колонки только для чтения
		"OMD_PCM_SOCK="+c.p.sock(),
		"OMD_SHIM_VOLUME="+shimVolume,
		"OMD_SHIM_LAT_US="+strconv.Itoa(cfg.LatencyMS*1000),
	)
	cmd.Stdout, cmd.Stderr = c.slog, c.slog
	cmd.SysProcAttr = sysProcAttr()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	os.WriteFile(c.p.pid(), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644)
	setNice(cmd.Process.Pid, soloistNice)
	return execProc{cmd}, nil
}

// startVolume — громкость, с которой стартует Soloist: громкость колонки (если она связана со Spotify) или volume из настроек.
func (c *Component) startVolume(cfg Config) int {
	if c.vol != nil {
		if p := c.vol.percent(); p >= 0 {
			return p
		}
	}
	return cfg.Volume
}

// Reload применяет изменённые настройки без перезапуска демона (демон зовёт его при `openmediad config set` и reload).
// Возвращает ключи, которым нужен перезапуск демона. device_name, cache_mb, latency_ms, verbose и volume — на ходу: Soloist
// перезапускается сам (несколько секунд тишины от Spotify, музыка Яндекса и Алиса не затрагиваются); keep_free_mb — сразу.
func (c *Component) Reload(newCfg any) (restart []string) {
	n, ok := newCfg.(*Config)
	if !ok || n == nil {
		return nil
	}
	if err := n.validate(); err != nil {
		c.logf("новые настройки Spotify отвергнуты: %v", err)
		return nil
	}
	c.cfgMu.Lock()
	old := c.cfg
	c.cfg = *n
	c.cfgMu.Unlock()
	for _, k := range []struct {
		name string
		diff bool
	}{
		{"dir", old.Dir != n.Dir}, {"key_file", old.KeyFile != n.KeyFile}, {"ws_port", old.WSPort != n.WSPort},
		{"fix_cp15", old.FixCP15 != n.FixCP15}, {"volume_sync", old.VolumeSync != n.VolumeSync},
	} {
		if k.diff {
			restart = append(restart, k.name)
		}
	}
	if c.cancel == nil { // не запущен (не настроен): новые настройки подхватятся при запуске
		return restart
	}
	if c.cache != nil {
		c.cache.setLimits(n.CacheMB, n.KeepFreeMB)
	}
	if old.VirtualPlayer != n.VirtualPlayer {
		c.virtualPush() // включили или выключили на ходу
	}
	if old.DeviceName != n.DeviceName || old.CacheMB != n.CacheMB || old.LatencyMS != n.LatencyMS || old.Verbose != n.Verbose ||
		old.Volume != n.Volume && c.vol == nil {
		if old.DeviceName != n.DeviceName {
			c.logf("колонка переименована в Spotify: «%s» → «%s» (Soloist перезапускается)", old.DeviceName, n.DeviceName)
		}
		c.sup.restart()
	}
	return restart
}

// onSoloistLine запоминает важные строки журнала Soloist: вход и срок жизни сборки.
func (c *Component) onSoloistLine(level, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case strings.Contains(text, "waiting for login"):
		c.login = "ждёт входа: выберите устройство в приложении Spotify"
	case strings.Contains(text, "UCS login result") && strings.Contains(text, "outcome=success"):
		c.login = "вход выполнен"
	case strings.Contains(text, "Access denied from backend"), strings.Contains(text, "session_access_denied"):
		c.login = "Spotify отказал во входе (ключ, аккаунт или регион)"
	case strings.HasPrefix(text, "client expires in"):
		c.expires = text
	}
}

// arbiter уступает звук: заиграл Spotify — музыка Яндекса на паузе; заиграл Яндекс — Spotify на паузе. Побеждает тот, кто
// заиграл позже. Опрос раз в 250 мс: хватает, чтобы два потока не звучали вместе дольше доли секунды.
func (c *Component) arbiter(ctx context.Context) {
	if c.mach == nil {
		return
	}
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	var prevS, prevY bool
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s, y := c.pcm.Playing(), c.mach.Playing()
		if s && !prevS { // кто заиграл последним, тому и достаётся «продолжи» после паузы (voice.go); при одновременном старте побеждает Spotify
			c.setOwner(ownerSpotify)
		} else if y && !prevY {
			c.setOwner(ownerYandex)
		}
		switch {
		case s && !prevS:
			// Яндекс играл — остановить; на паузе у платформы остался его плеер — отдать место Spotify (видно на дисплее)
			if n := c.mach.TakeOver(); n > 0 {
				c.noteYield()
				c.logf("заиграл Spotify: музыка Яндекса (плееров: %d) остановлена", n)
			}
			y = false
		case y && !prevY && s:
			if err := c.ctl.Pause(); err != nil {
				c.logf("заиграл Яндекс, но Spotify на паузу поставить не вышло: %v", err)
			} else {
				c.noteYield()
				c.logf("заиграл Яндекс: Spotify поставлен на паузу")
			}
		}
		prevS, prevY = s, y
	}
}

func (c *Component) noteYield() {
	c.mu.Lock()
	c.yields++
	c.mu.Unlock()
}

// StatusLine — строка для `openmediad status`.
func (c *Component) StatusLine() string {
	c.mu.Lock()
	needs, login, yields, voice := c.needs, c.login, c.yields, c.voiceHandled
	c.mu.Unlock()
	if len(needs) > 0 {
		return "не настроен: нет " + strings.Join(needs, ", ") + " — openmediad spotify setup / setkey"
	}
	sup := c.sup.status()
	if sup.Expired {
		return "сборка Soloist истекла: openmediad spotify update"
	}
	ps := c.ctl.state()
	var parts []string
	switch {
	case sup.Pid == 0:
		parts = append(parts, "Soloist: "+sup.State)
	case !ps.Connected:
		parts = append(parts, "Soloist запускается")
	case ps.LoggedIn:
		parts = append(parts, fmt.Sprintf("устройство «%s» в списке Spotify", c.cfg.DeviceName))
	default:
		parts = append(parts, orDefault(login, "ждёт входа: выберите устройство «"+c.cfg.DeviceName+"» в приложении Spotify"))
	}
	if ps.Status == "playing" && ps.Title != "" {
		parts = append(parts, "играет: "+strings.TrimPrefix(ps.Artist+" — ", " — ")+ps.Title)
	} else if ps.Status != "" && ps.LoggedIn {
		parts = append(parts, ps.Status)
	}
	if sup.Hangs > 0 {
		parts = append(parts, fmt.Sprintf("зависаний: %d", sup.Hangs))
	}
	if yields > 0 {
		parts = append(parts, fmt.Sprintf("уступал звук: %d", yields))
	}
	if voice > 0 {
		parts = append(parts, fmt.Sprintf("голосом выполнено: %d", voice))
	}
	return strings.Join(parts, "; ")
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// State — снимок для `status -v` и отладочной страницы.
type State struct {
	NeedsSetup []string    `json:"needs_setup,omitempty"`
	CP15       string      `json:"cp15"`
	CP15Now    string      `json:"cp15_now"`
	Expires    string      `json:"expires,omitempty"`
	Proc       supStatus   `json:"process"`
	Player     playerState `json:"player"`
	PCM        pcmStatus   `json:"pcm"`
	Cache      cacheStatus `json:"cache"`
	Volume     *volStatus  `json:"volume,omitempty"` // связь громкости Spotify с колонкой
	Voice      voiceStatus `json:"voice"`            // голосовое управление (перехват фраз)
	Log        []string    `json:"soloist_log_tail,omitempty"`
}

// DebugState — состояние компонента.
func (c *Component) DebugState() any {
	c.mu.Lock()
	st := State{NeedsSetup: c.needs, CP15: c.cp15, Expires: c.expires}
	c.mu.Unlock()
	st.CP15Now = cp15Value(layout.Root)
	st.Voice = c.voiceStatus()
	if c.sup != nil {
		st.Proc, st.Player, st.PCM, st.Cache = c.sup.status(), c.ctl.state(), c.pcm.status(), c.cache.status()
		st.Log = c.slog.lines()
		if c.vol != nil {
			vs := c.vol.status()
			st.Volume = &vs
		}
	}
	return st
}
