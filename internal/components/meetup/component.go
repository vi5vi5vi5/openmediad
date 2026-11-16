// Package meetup — колонка как участник конференции MeetUp (https://github.com/vi5vi5vi5/MeetUp): звук движка (музыка,
// голос Алисы) уходит в комнату, команды из чата управляют плеером, как у музыкальных ботов в Discord.
//
// Компонент — выход звука «meetup» для [route] и служба: держит соединение с комнатой, читает чат, отдаёт страницу настроек.
// Что пока заглушки — в README.md, раздел «Заглушки».
package meetup

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tphakala/go-opus/opus"

	"openmediad/internal/components/meetup/proto"
	"openmediad/internal/core/audio"
	"openmediad/internal/core/control"
	"openmediad/internal/core/led"
	"openmediad/internal/core/quasar/mediad"
)

// Name — имя компонента и выхода звука в [route].
const Name = "meetup"

const (
	sendQueue  = 50                     // очередь звука к сети: 1 с; не помещается — кадр пропадает, движок не ждёт
	staleAfter = 400 * time.Millisecond // кадр, простоявший в очереди дольше, не досылается (сеть застряла и отпустила)
	silentTail = 25                     // после стольких кадров тишины подряд (0,5 с) кодер и сеть отдыхают
	maxRetry   = 30 * time.Second       // самая длинная пауза между попытками войти
)

// Сроки (переменные, а не константы: тесты их укорачивают).
var (
	micOffAfter = 3 * time.Second         // столько без звука — «микрофон» колонки в комнате показывается выключенным
	holdFor     = 15 * time.Second        // столько потоки ждут переподключения, прежде чем уйти на следующий выход маршрута
	offlineWait = 5 * time.Second         // как часто стучаться в личную комнату, которую владелец ещё не открыл
	firstRetry  = 1500 * time.Millisecond // первая пауза перед повторным входом; дальше удваивается до maxRetry
)

// Откуда взят ключ сквозного шифрования (поле e2e в состоянии).
const (
	e2eLink   = "link"   // из ссылки-приглашения (#k=…)
	e2ePhrase = "phrase" // выведен из фразы
)

// Фазы подключения (поле phase в состоянии).
const (
	phaseOff        = "off"        // подключение не запрошено
	phaseConnecting = "connecting" // идёт вход
	phaseJoined     = "joined"     // в комнате
	phaseWaiting    = "waiting"    // ждём повтора (нет связи, комната не открыта)
	phaseFailed     = "failed"     // окончательный отказ: нужна другая ссылка или настройки
)

type packet struct {
	at   time.Time
	data []byte
}

// Component — компонент meetup.
type Component struct {
	logf func(format string, a ...any)
	hub  *control.Hub
	mach *mediad.Machine
	// ask говорит Алисе фразу текстом, как голосом (подменяется в тестах).
	ask  func(ctx context.Context, text string) error
	path string // файл состояния
	tls  *tls.Config

	mix *mixer
	// только горутина смесителя:
	enc       *opus.Encoder
	encCh     int     // на сколько каналов открыт кодер
	mono      []int16 // кадр, сведённый в моно (когда stereo выключено)
	encBuf    []byte
	silentRun int
	encStale  atomic.Bool // настройки кодера изменились: пересоздать

	mu        sync.Mutex
	cfg       Config
	set       Settings
	gen       uint64 // растёт, когда меняется то, с чем входят в комнату: ссылка, имя, «подключаться»
	sess      *proto.Session
	queue     chan packet
	texts     chan string
	stopSend  chan struct{}
	link      proto.Link
	phase     string
	errText   string
	holdUntil time.Time
	holdTimer *time.Timer
	arrival   func()
	bucket    bucket
	e2e       string          // шифрование текущей сессии: "", e2eLink или e2ePhrase
	acctNote  string          // что не так с аккаунтом в текущей сессии ("" — всё в порядке)
	inAccount bool            // текущая сессия привязана к аккаунту
	told      map[uint32]bool // кому в этой сессии уже сказано, что колонка его не слушается
	kdf       struct {        // последний ключ, выведенный из фразы: вывод стоит долей секунды
		phrase, room string
		key          []byte
	}

	wake   chan struct{}
	jobs   chan func()
	cancel context.CancelFunc
	wg     sync.WaitGroup

	sent, dropped, commands, encrypted atomic.Uint64
}

func newComponent(cfg Config, path string, logf func(string, ...any)) *Component {
	c := &Component{logf: logf, path: path, cfg: cfg, phase: phaseOff, wake: make(chan struct{}, 1), jobs: make(chan func(), 8),
		encBuf: make([]byte, 1500), bucket: newBucket(bucketSize)}
	set, err := loadSettings(path, cfg.Name)
	if err != nil {
		logf("настройки конференции не прочитаны (%v): беру умолчания", err)
	}
	c.set = set
	c.mix = startMixer(c.frame)
	c.mix.setGain(volumeGain(set.Volume))
	return c
}

// volumeGain — усиление для громкости 0–100: квадратичная шкала, на слух ближе к равномерной (50 — четверть амплитуды).
func volumeGain(v int) float64 { return float64(v) * float64(v) / 10000 }

// ---- выход звука (daemon.Sink) ----

// Name — имя выхода в [route].
func (c *Component) Name() string { return Name }

// Available — звук сейчас можно отдавать в конференцию: колонка в комнате (или вот-вот вернётся) и вывод звука включён.
func (c *Component) Available() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.availableLocked()
}

func (c *Component) availableLocked() bool {
	return c.set.Audio && (c.sess != nil || time.Now().Before(c.holdUntil))
}

// Open — поток движка в конференцию.
func (c *Component) Open(f audio.Format) (audio.Output, error) {
	if !c.Available() {
		return nil, errKicked
	}
	return c.mix.Open(f)
}

// OnArrival — демон даёт функцию «выход стал доступен»: звучащие потоки переходят в конференцию.
func (c *Component) OnArrival(f func()) {
	c.mu.Lock()
	c.arrival = f
	c.mu.Unlock()
}

// LEDColor — цвет подсветки «музыка идёт в конференцию»: лаймовый, как акцент MeetUp.
func (c *Component) LEDColor() led.Color { return led.Color{R: 170, G: 255, B: 0} }

// frame получает от смесителя кадр 20 мс (стерео): кодирует и ставит в очередь к сети. Никогда не ждёт сеть.
func (c *Component) frame(pcm []int16, silent bool) {
	if silent {
		if c.silentRun++; c.silentRun > silentTail {
			return
		}
	} else {
		c.silentRun = 0
	}
	c.mu.Lock()
	q, cfg, sess := c.queue, c.cfg, c.sess
	c.mu.Unlock()
	if q == nil { // переподключение: звук пропадает, поток движка идёт дальше
		return
	}
	// Слушать некому (колонка одна в комнате, или у всех её громкость в нуле): сервер всё равно выбросил бы кадр, поэтому
	// не тратим на него ни кодер, ни сеть. Музыка при этом идёт дальше в своём темпе.
	if sess != nil && !sess.Wanted() {
		return
	}
	ch := 1
	if cfg.Stereo {
		ch = 2
	}
	if c.enc == nil || c.encCh != ch || c.encStale.Swap(false) {
		enc, err := opus.NewEncoder(opus.EncoderConfig{SampleRate: audio.Rate, Channels: ch, Bitrate: cfg.Bitrate, Complexity: cfg.Complexity})
		if err != nil {
			c.dropped.Add(1)
			return
		}
		c.enc, c.encCh = enc, ch
	}
	if ch == 1 { // стерео → моно: полусумма каналов
		if c.mono == nil {
			c.mono = make([]int16, len(pcm)/2)
		}
		for i := range c.mono {
			c.mono[i] = int16((int32(pcm[2*i]) + int32(pcm[2*i+1])) / 2)
		}
		pcm = c.mono
	}
	n, err := c.enc.Encode(pcm, c.encBuf)
	if err != nil || n == 0 {
		c.dropped.Add(1)
		return
	}
	select {
	case q <- packet{at: time.Now(), data: append([]byte(nil), c.encBuf[:n]...)}:
	default:
		c.dropped.Add(1)
	}
}

// sendLoop — единственный, кто пишет в соединение сессии: звук, сообщения в чат, состояние «микрофона».
func (c *Component) sendLoop(sess *proto.Session, q <-chan packet, texts <-chan string, stop <-chan struct{}) {
	defer c.wg.Done()
	mic := false
	var last time.Time
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		var err error
		select {
		case <-stop:
			return
		case p := <-q:
			if time.Since(p.at) > staleAfter {
				c.dropped.Add(1)
				continue
			}
			if !mic {
				mic = true
				err = sess.SetMic(true)
			}
			if err == nil {
				err = sess.SendAudio(p.data, p.at)
			}
			c.sent.Add(1)
			last = time.Now()
		case t := <-texts:
			err = sess.SendChat(t)
		case <-tick.C:
			if mic && time.Since(last) > micOffAfter {
				mic = false
				err = sess.SetMic(false)
			}
		}
		if err != nil {
			sess.Close() // чтение заметит разрыв и начнёт переподключение
			return
		}
	}
}

// say ставит сообщение в очередь к чату; false — колонка не в комнате или очередь полна.
func (c *Component) say(text string) bool {
	c.mu.Lock()
	t := c.texts
	c.mu.Unlock()
	if t == nil {
		return false
	}
	select {
	case t <- text:
		return true
	default:
		return false
	}
}

// ---- подключение ----

// Start запускает подключение, обработчик команд чата и страницу настроек.
func (c *Component) Start(ctx context.Context) error {
	ctx, c.cancel = context.WithCancel(ctx)
	c.wg.Add(2)
	go func() { defer c.wg.Done(); c.run(ctx) }()
	go func() { defer c.wg.Done(); c.work(ctx) }()
	if addr := c.config().Listen; addr != "" {
		go func() {
			if err := c.listen(ctx, addr); err != nil {
				c.logf("страница настроек %s: %v", addr, err)
			}
		}()
		c.logf("страница настроек конференции: http://%s/", addr)
	}
	return nil
}

// Stop выходит из комнаты и останавливает всё.
func (c *Component) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Lock()
	sess := c.sess
	if c.holdTimer != nil {
		c.holdTimer.Stop()
	}
	c.mu.Unlock()
	if sess != nil {
		sess.Close()
	}
	c.wg.Wait()
	c.mix.Stop()
}

func (c *Component) config() Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// work выполняет команды чата по одной: они ходят к Алисе и к платформе и могут задержаться.
func (c *Component) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-c.jobs:
			job()
		}
	}
}

func (c *Component) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// pause ждёт d (0 — без срока) или сигнала «настройки изменились»; false — демон останавливается.
func (c *Component) pause(ctx context.Context, d time.Duration) bool {
	var timer <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-ctx.Done():
		return false
	case <-c.wake:
	case <-timer:
	}
	return true
}

func (c *Component) setPhase(phase, errText string) {
	c.mu.Lock()
	changed := c.phase != phase || c.errText != errText
	c.phase, c.errText = phase, errText
	c.mu.Unlock()
	if changed && errText != "" {
		c.logf("конференция: %s", errText)
	}
}

// run держит колонку в комнате: входит, читает, после обрыва входит снова; после окончательного отказа ждёт новых настроек.
func (c *Component) run(ctx context.Context) {
	var retry time.Duration
	var greeted uint64
	for ctx.Err() == nil {
		c.mu.Lock()
		set, gen := c.set, c.gen
		c.mu.Unlock()
		if !set.Connect || set.Link == "" {
			c.setPhase(phaseOff, "")
			c.release()
			if !c.pause(ctx, 0) {
				return
			}
			continue
		}
		link, err := proto.ParseLink(set.Link)
		if err != nil {
			c.setPhase(phaseFailed, err.Error())
			c.release()
			if !c.pause(ctx, 0) {
				return
			}
			continue
		}
		c.mu.Lock()
		c.link = link
		if c.phase != phaseWaiting {
			c.phase, c.errText = phaseConnecting, ""
		}
		c.mu.Unlock()
		opts, e2e, err := c.joinOptions(link, set)
		if err != nil { // ключ шифрования в ссылке испорчен: открытым текстом в такую комнату не идём
			c.setPhase(phaseFailed, err.Error())
			c.release()
			if !c.pause(ctx, 0) {
				return
			}
			continue
		}

		sess, err := proto.Join(ctx, link, opts)
		if err == nil && c.generation() != gen { // пока входили, ссылку или имя поменяли
			sess.Close()
			continue
		}
		if err == nil {
			retry = 0
			note := ""
			if opts.Session != "" && !sess.Account() {
				note = "Сессия аккаунта истекла: колонка вошла без аккаунта. Войдите в аккаунт заново."
			}
			c.attach(sess, link, e2e, note)
			if greeted != gen {
				greeted = gen
				c.say(c.hello())
			}
			err = sess.Run(proto.Handler{Chat: c.onChat, Notice: func(reason string) { c.logf("конференция: сервер сообщил «%s»", reason) }})
			c.detach(c.generation() == gen && !fatalError(err) && ctx.Err() == nil)
			if ctx.Err() != nil {
				return
			}
			if c.generation() != gen {
				continue
			}
		}
		var je *proto.JoinError
		switch {
		case ctx.Err() != nil:
			return
		case fatalError(err):
			c.setPhase(phaseFailed, err.Error())
			c.release()
			if !c.pause(ctx, 0) {
				return
			}
		case errors.As(err, &je) && je.Reason == "room_offline":
			c.setPhase(phaseWaiting, err.Error())
			if !c.pause(ctx, offlineWait) {
				return
			}
		default:
			retry = min(max(retry*2, firstRetry), maxRetry)
			c.setPhase(phaseWaiting, fmt.Sprintf("связь с конференцией потеряна (%v): повтор через %v", err, retry.Round(100*time.Millisecond)))
			if !c.pause(ctx, retry) {
				return
			}
		}
	}
}

func fatalError(err error) bool {
	var je *proto.JoinError
	return errors.As(err, &je) && proto.Fatal(je.Reason)
}

func (c *Component) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// attach — колонка вошла в комнату: звук и чат идут в эту сессию.
func (c *Component) attach(sess *proto.Session, link proto.Link, e2e, acctNote string) {
	stop := make(chan struct{})
	c.encrypted.Store(0)
	c.mu.Lock()
	c.sess, c.link = sess, link
	c.e2e, c.acctNote, c.inAccount, c.told = e2e, acctNote, sess.Account(), map[uint32]bool{}
	c.queue, c.texts = make(chan packet, sendQueue), make(chan string, 16)
	c.stopSend = stop
	c.phase, c.errText = phaseJoined, ""
	c.holdUntil = time.Time{}
	if c.holdTimer != nil {
		c.holdTimer.Stop()
		c.holdTimer = nil
	}
	arrival, audioOn := c.arrival, c.set.Audio
	q, texts := c.queue, c.texts
	c.mu.Unlock()
	c.wg.Add(1)
	go c.sendLoop(sess, q, texts, stop)
	how := "без аккаунта"
	if sess.Account() {
		how = "под аккаунтом"
	}
	switch e2e {
	case e2eLink:
		how += ", сквозное шифрование ключом из ссылки"
	case e2ePhrase:
		how += ", сквозное шифрование по фразе"
	}
	c.logf("конференция: вошла в комнату (%s) %s, участников кроме колонки: %d", link.Display(), how, len(sess.Participants()))
	if acctNote != "" {
		c.logf("конференция: %s", acctNote)
	}
	if arrival != nil && audioOn {
		go arrival()
	}
}

// joinOptions собирает то, с чем колонка входит в комнату: имя, пароль комнаты, сессию аккаунта (только серверу, который её
// выдал) и шифр. Ключ шифрования — из ссылки (#k=…), а если его там нет — из фразы. Ошибка — ключ в ссылке испорчен.
func (c *Component) joinOptions(link proto.Link, set Settings) (o proto.Options, e2e string, err error) {
	c.mu.Lock()
	wsURL := c.cfg.WSURL
	c.mu.Unlock()
	o = proto.Options{Name: set.Name, TLS: c.tls, WSURL: wsURL, Password: set.RoomPassword}
	if a := set.Account; a.Token != "" && a.Host == link.Host && a.Secure == link.Secure {
		o.Session = a.Token
	}
	var key []byte
	switch {
	case link.Key != "":
		if key, err = proto.KeyFromLink(link.Key); err != nil {
			return o, "", err
		}
		e2e = e2eLink
	case set.Phrase != "":
		key, e2e = c.phraseKey(set.Phrase, link.Room), e2ePhrase
	}
	if key != nil {
		if o.Cipher, err = proto.NewCipher(key); err != nil {
			return o, "", err
		}
	}
	return o, e2e, nil
}

// phraseKey выводит ключ из фразы и помнит последний: при переподключении его не считают заново.
func (c *Component) phraseKey(phrase, room string) []byte {
	c.mu.Lock()
	if c.kdf.key != nil && c.kdf.phrase == phrase && c.kdf.room == room {
		key := c.kdf.key
		c.mu.Unlock()
		return key
	}
	c.mu.Unlock()
	key := proto.KeyFromPhrase(phrase, room)
	c.mu.Lock()
	c.kdf.phrase, c.kdf.room, c.kdf.key = phrase, room, key
	c.mu.Unlock()
	return key
}

// Login входит в аккаунт MeetUp на сервере из сохранённой ссылки и запоминает сессию; пароль не хранится.
func (c *Component) Login(ctx context.Context, login, password string) error {
	c.mu.Lock()
	raw := c.set.Link
	c.mu.Unlock()
	if raw == "" {
		return errors.New("сначала задайте ссылку на конференцию: по ней колонка узнаёт сервер")
	}
	link, err := proto.ParseLink(raw)
	if err != nil {
		return err
	}
	acc, err := proto.Login(ctx, link, login, password, c.tls)
	if err != nil {
		return err
	}
	c.logf("конференция: вход в аккаунт на %s выполнен", link.Host)
	_, err = c.Update(func(s *Settings) {
		s.Account = SavedAccount{Host: link.Host, Secure: link.Secure, Login: acc.Login, Name: acc.Name, UserID: acc.UserID, Token: acc.Token}
	})
	return err
}

// Logout завершает сессию аккаунта на сервере и забывает её.
func (c *Component) Logout(ctx context.Context) {
	c.mu.Lock()
	a := c.set.Account
	c.mu.Unlock()
	if a.Token == "" {
		return
	}
	if err := proto.Logout(ctx, proto.Link{Secure: a.Secure, Host: a.Host}, a.Token, c.tls); err != nil {
		c.logf("конференция: сервер не подтвердил выход из аккаунта (%v): сессия забыта только на колонке", err)
	}
	c.Update(func(s *Settings) { s.Account = SavedAccount{} })
}

// detach — сессия кончилась. hold — это обрыв связи, и колонка будет входить снова: потоки движка ждут в смесителе holdFor
// (иначе на каждом коротком обрыве музыка перескакивала бы на динамики), и только потом уходят на следующий выход.
func (c *Component) detach(hold bool) {
	c.mu.Lock()
	sess, stop := c.sess, c.stopSend
	c.sess, c.queue, c.texts, c.stopSend = nil, nil, nil, nil
	if hold && c.set.Audio && c.mix.Streams() > 0 {
		c.holdUntil = time.Now().Add(holdFor)
		c.holdTimer = time.AfterFunc(holdFor, func() {
			c.mu.Lock()
			gone := c.sess == nil
			c.mu.Unlock()
			if gone {
				c.mix.Kick()
			}
		})
	}
	c.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if sess != nil {
		sess.Close()
	}
	if !hold {
		c.release()
	}
}

// release отпускает потоки движка на следующий выход маршрута (динамики).
func (c *Component) release() {
	c.mu.Lock()
	c.holdUntil = time.Time{}
	if c.holdTimer != nil {
		c.holdTimer.Stop()
		c.holdTimer = nil
	}
	c.mu.Unlock()
	c.mix.Kick()
}

// ---- настройки на ходу ----

// Update меняет настройки (страница, команда терминала, чат): edit получает текущие и правит их. Сохраняет в файл состояния
// и применяет сразу.
func (c *Component) Update(edit func(*Settings)) (Settings, error) {
	c.mu.Lock()
	old := c.set
	set := old
	set.Rules = slices.Clone(old.Rules) // правка не должна задеть действующие правила до конца проверки
	edit(&set)
	set.normalize(c.cfg.Name)
	if set.Connect && set.Link != "" {
		if _, err := proto.ParseLink(set.Link); err != nil {
			c.mu.Unlock()
			return old, err
		}
	}
	c.set = set
	rejoin := set.Link != old.Link || set.Name != old.Name || set.Connect != old.Connect ||
		set.RoomPassword != old.RoomPassword || set.Phrase != old.Phrase || set.Account.Token != old.Account.Token
	if rejoin {
		c.gen++
	}
	sess, arrival := c.sess, c.arrival
	c.mu.Unlock()

	c.mix.setGain(volumeGain(set.Volume))
	switch {
	case rejoin:
		if sess != nil {
			sess.Close()
		}
		c.poke()
	case old.Audio && !set.Audio:
		c.release()
	case !old.Audio && set.Audio && sess != nil && arrival != nil:
		go arrival()
	}
	if err := saveSettings(c.path, set); err != nil {
		c.logf("настройки конференции не сохранены (%v): действуют до перезапуска", err)
	}
	return set, nil
}

// Reload применяет изменённую секцию [meetup] файла настроек.
func (c *Component) Reload(cfg any) (restart []string) {
	n, ok := cfg.(*Config)
	if !ok {
		return nil
	}
	c.mu.Lock()
	old := c.cfg
	next := *n
	next.Listen, next.WSURL = old.Listen, old.WSURL // эти два — только с перезапуском
	c.cfg = next
	c.mu.Unlock()
	if n.Listen != old.Listen {
		restart = append(restart, "listen")
	}
	if n.WSURL != old.WSURL {
		restart = append(restart, "ws_url")
	}
	if n.Bitrate != old.Bitrate || n.Complexity != old.Complexity || n.Stereo != old.Stereo {
		c.encStale.Store(true)
	}
	return restart
}

// ---- состояние ----

// State — состояние компонента для страницы настроек, `openmediad meetup` и `status -v`. Ссылки и кода комнаты в нём нет.
type State struct {
	Phase        string   `json:"phase"`
	Status       string   `json:"status"`
	Error        string   `json:"error,omitempty"`
	Room         string   `json:"room,omitempty"`
	Title        string   `json:"title,omitempty"`
	E2E          string   `json:"e2e,omitempty"`     // шифрование сессии: "link" — ключ из ссылки, "phrase" — по фразе
	HasLink      bool     `json:"has_link"`          // ссылка на конференцию сохранена
	HasPassword  bool     `json:"has_password"`      // пароль комнаты задан
	HasPhrase    bool     `json:"has_phrase"`        // фраза шифрования задана
	Account      string   `json:"account,omitempty"` // сохранённый вход: «логин на сервере»
	AccountInUse bool     `json:"account_in_use"`    // текущая сессия идёт под аккаунтом
	Name         string   `json:"name"`              // имя участника у входа без аккаунта
	Connect      bool     `json:"connect"`
	Audio        bool     `json:"audio"`
	Chat         bool     `json:"chat"`
	Alice        bool     `json:"alice"`
	Volume       int      `json:"volume"`
	ObeyNew      bool     `json:"obey_new"`        // слушаться участников без личного правила
	Participants []Person `json:"participants"`    // кто сейчас в комнате, кроме колонки
	Rules        []Rule   `json:"rules"`           // все личные правила, включая отсутствующих
	Streams      int      `json:"streams"`         // потоков движка в конференции сейчас
	Sent         uint64   `json:"frames_sent"`     // звуковых кадров отправлено
	Dropped      uint64   `json:"frames_dropped"`  // кадров пропало (сеть не успевала)
	Commands     uint64   `json:"commands"`        // команд чата выполнено
	Encrypted    uint64   `json:"encrypted_chats"` // сообщений чата в этой сессии не прочитано: зашифрованы
	SkippedMB    float64  `json:"skipped_mb"`      // чужого звука и видео прочитано и выброшено, МБ
	RTTms        int64    `json:"rtt_ms"`          // задержка до сервера; −1 — не измерена
	Hint         string   `json:"hint,omitempty"`  // подсказка, что сделать человеку
}

// Person — участник комнаты на странице настроек.
type Person struct {
	Name    string `json:"name"`
	Account bool   `json:"account"` // вошёл под аккаунтом (имя подтверждено сервером)
	Key     string `json:"key"`     // ключ личного правила
	Obey    bool   `json:"obey"`    // слушается ли колонка его команд сейчас
	Ruled   bool   `json:"ruled"`   // есть личное правило (иначе действует правило для новых)
}

// Snapshot — текущее состояние.
func (c *Component) Snapshot() State {
	c.mu.Lock()
	set, sess, link, phase, errText := c.set, c.sess, c.link, c.phase, c.errText
	e2e, acctNote, inAccount := c.e2e, c.acctNote, c.inAccount
	c.mu.Unlock()
	st := State{Phase: phase, Error: errText, HasLink: set.Link != "", HasPassword: set.RoomPassword != "", HasPhrase: set.Phrase != "",
		Name: set.Name, Connect: set.Connect, Audio: set.Audio, Chat: set.Chat, Alice: set.Alice, Volume: set.Volume, ObeyNew: set.ObeyNew,
		Participants: []Person{}, Rules: append([]Rule{}, set.Rules...), Streams: c.mix.Streams(), Sent: c.sent.Load(),
		Dropped: c.dropped.Load(), Commands: c.commands.Load(), RTTms: -1}
	if set.Account.Token != "" {
		st.Account = set.Account.Login + " на " + set.Account.Host
	}
	if phase != phaseOff && link.Host != "" {
		st.Room = link.Display()
	}
	if sess != nil {
		st.E2E, st.AccountInUse, st.Encrypted = e2e, inAccount, c.encrypted.Load()
		for _, p := range sess.Participants() {
			uid := proto.UserIDOf(p.ID)
			key := ruleKey(link.Host, uid, p.Name)
			obey, ruled := set.obeys(key)
			st.Participants = append(st.Participants, Person{Name: p.Name, Account: uid > 0, Key: key, Obey: obey, Ruled: ruled})
		}
		st.Title = sess.Title()
		_, bytes := sess.Skipped()
		st.SkippedMB = float64(bytes>>10) / 1024
		if d, ok := sess.RTT(); ok {
			st.RTTms = d.Milliseconds()
		}
	}
	switch phase {
	case phaseOff:
		st.Status = "не подключена"
	case phaseConnecting:
		st.Status = "входит в комнату"
	case phaseJoined:
		st.Status = fmt.Sprintf("в комнате, участников кроме колонки: %d", len(st.Participants))
	case phaseWaiting:
		st.Status = "ждёт повтора"
	case phaseFailed:
		st.Status = "не вошла"
	}
	switch {
	case sess == nil:
	case acctNote != "":
		st.Hint = acctNote
	case st.Encrypted > 0 && e2e == "":
		st.Hint = "В комнате пишут зашифрованные сообщения, а ключа у колонки нет: задайте фразу шифрования или ссылку-приглашение с ключом."
	case st.Encrypted > 0:
		st.Hint = "Часть сообщений чата не расшифрована: у их авторов другой ключ (или у колонки не та фраза)."
	}
	return st
}

// DebugState — данные для `status -v` и отладочной страницы.
func (c *Component) DebugState() any { return c.Snapshot() }

// StatusLine — строка для `openmediad status`.
func (c *Component) StatusLine() string {
	st := c.Snapshot()
	line := st.Status
	if st.Room != "" {
		line += " (" + st.Room + ")"
	}
	if st.Error != "" {
		line += ": " + st.Error
	}
	if st.Phase == phaseJoined {
		switch {
		case !st.Audio:
			line += "; звук в конференцию выключен"
		case st.Streams > 0:
			line += fmt.Sprintf("; звук идёт в конференцию (потоков: %d)", st.Streams)
		}
		if !st.Chat {
			line += "; команды чата выключены"
		}
		if st.AccountInUse {
			line += "; под аккаунтом"
		}
		if st.E2E != "" {
			line += "; шифрование включено"
		}
	}
	return line
}
