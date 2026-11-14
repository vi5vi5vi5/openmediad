package meetup

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tphakala/go-opus/opus"

	"openmediad/internal/components/meetup/proto/prototest"
	"openmediad/internal/core/audio"
	"openmediad/internal/core/control"
	"openmediad/internal/core/control/controltest"
)

// roomCode — код разовой комнаты, как у настоящего сервера (24 символа): он секрет и в журнал целиком не попадает.
const roomCode = "AbCdEfGhIjKlMnOpQrStUvWx"

// stand — компонент, подключённый к поддельному серверу MeetUp, поддельному плееру и поддельной Алисе.
type stand struct {
	t      *testing.T
	c      *Component
	srv    *prototest.Server
	player *controltest.Player

	mu       sync.Mutex
	asked    []string
	arrivals int
}

func newStand(t *testing.T) *stand {
	t.Helper()
	shorten(t, &micOffAfter, 300*time.Millisecond)
	shorten(t, &firstRetry, 50*time.Millisecond)
	shorten(t, &offlineWait, 50*time.Millisecond)
	s := &stand{t: t, srv: prototest.New(t), player: controltest.NewPlayer(controltest.Playing())}
	cfg := *defaults()
	cfg.Listen = ""
	cfg.WSURL = s.srv.WSURL()
	s.c = newComponent(cfg, filepath.Join(t.TempDir(), "state", "meetup.json"), t.Logf)
	s.c.bucket = newBucket(1000) // частоту команд проверяет TestChatFlood
	s.c.hub = &control.Hub{}
	s.c.hub.Add("плеер", s.player)
	s.c.ask = func(_ context.Context, text string) error {
		s.mu.Lock()
		s.asked = append(s.asked, text)
		s.mu.Unlock()
		return nil
	}
	s.c.OnArrival(func() { s.mu.Lock(); s.arrivals++; s.mu.Unlock() })
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); s.c.Stop() })
	return s
}

func shorten(t *testing.T, v *time.Duration, d time.Duration) {
	old := *v
	*v = d
	t.Cleanup(func() { *v = old })
}

func (s *stand) join() {
	s.t.Helper()
	if _, err := s.c.Update(func(set *Settings) { set.Link, set.Connect = s.srv.Link(roomCode), true }); err != nil {
		s.t.Fatal(err)
	}
	s.waitPhase(phaseJoined)
	if hello := recv(s.t, s.srv.Chats, "приветствие в чате"); !strings.Contains(hello, "!help") {
		s.t.Fatalf("приветствие: %q", hello)
	}
}

func (s *stand) waitPhase(phase string) {
	s.t.Helper()
	wait(s.t, "фаза "+phase, func() bool { return s.c.Snapshot().Phase == phase })
}

func (s *stand) askedPhrases() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

func (s *stand) arrived() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.arrivals
}

// command пишет в чат от имени участницы и возвращает ответ колонки.
func (s *stand) command(text string) string {
	s.t.Helper()
	s.srv.Say(3, "Анна", text)
	return recv(s.t, s.srv.Chats, "ответ на "+text)
}

func wait(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("не дождались: %s", what)
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("не дождались: %s", what)
		panic("unreachable")
	}
}

func quiet[T any](t *testing.T, ch <-chan T, d time.Duration, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("%s: пришло %v", what, v)
	case <-time.After(d):
	}
}

// tone — d звука 440 Гц в формате f.
func tone(f audio.Format, d time.Duration) []byte {
	n := int(d * time.Duration(f.Rate) / time.Second)
	out := make([]byte, 0, n*f.FrameBytes())
	for i := 0; i < n; i++ {
		v := uint16(int16(12000 * math.Sin(2*math.Pi*440*float64(i)/float64(f.Rate))))
		for ch := 0; ch < f.Channels; ch++ {
			out = binary.LittleEndian.AppendUint16(out, v)
		}
	}
	return out
}

// Главный путь: звук потока движка доходит до комнаты кадрами Opus по 20 мс моно, «микрофон» включается со звуком и
// выключается в тишине.
func TestSoundReachesTheRoom(t *testing.T) {
	s := newStand(t)
	if s.c.Available() {
		t.Fatal("выход доступен до входа в комнату")
	}
	if _, err := s.c.Open(audio.Stereo48); err == nil {
		t.Fatal("поток открылся до входа в комнату")
	}
	s.join()
	if !s.c.Available() || s.arrived() != 1 {
		t.Fatalf("после входа: доступен %v, сообщений «выход появился» %d", s.c.Available(), s.arrived())
	}
	if j := s.srv.Joins()[0]; j["name"] != "Колонка" || j["mic"] != false {
		t.Fatalf("join: %v", j)
	}

	out, err := s.c.Open(audio.Stereo48)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := out.Write(tone(audio.Stereo48, 600*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	out.Drain()
	if took := time.Since(start); took < 400*time.Millisecond || took > 2*time.Second {
		t.Fatalf("0,6 с звука ушли за %v: смеситель не держит темп", took)
	}
	if !recv(t, s.srv.Mic, "микрофон включён") {
		t.Fatal("первым пришло mic=false")
	}

	dec, err := opus.NewDecoder(48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, 5760)
	var frames, loud int
	for frames < 25 {
		f := recv(t, s.srv.Audio, "звуковой кадр")
		if f.Type != 4 || f.Codec != 4 || f.Flags != 0 {
			t.Fatalf("кадр: тип %d, кодек %d, флаги %d", f.Type, f.Codec, f.Flags)
		}
		if d := time.Since(time.UnixMilli(int64(f.TimeMS))); d < -time.Second || d > 5*time.Second {
			t.Fatalf("время кадра отличается от часов на %v", d)
		}
		n, err := dec.Decode(f.Payload, pcm)
		if err != nil || n != 960 {
			t.Fatalf("кадр декодируется в %d отсчётов (%v): клиенты MeetUp примут только 960", n, err)
		}
		var peak int16
		for _, v := range pcm[:n] {
			peak = max(peak, v)
		}
		if peak > 1000 {
			loud++
		}
		frames++
	}
	// громкость 50 % — четверть амплитуды: 12000 → ≈3000
	if loud < 15 {
		t.Fatalf("из %d кадров со звуком только %d", frames, loud)
	}
	if recv(t, s.srv.Mic, "микрофон выключен в тишине") {
		t.Fatal("в тишине пришло mic=true")
	}
	st := s.c.Snapshot()
	if st.Sent < 25 || st.Streams != 0 || st.Phase != phaseJoined || len(st.Participants) != 1 {
		t.Fatalf("состояние: %+v", st)
	}
	if line := s.c.StatusLine(); !strings.Contains(line, "в комнате") || strings.Contains(line, roomCode) {
		t.Fatalf("строка состояния: %q", line)
	}
}

// Команды чата: плеер — через ядро команд, запуск музыки и свободные фразы — Алисе текстом; история чата, собственные
// сообщения и выключенный чат команд не дают.
func TestChatCommands(t *testing.T) {
	s := newStand(t)
	s.join()
	if n := s.player.Count(); n != 0 {
		t.Fatalf("команда из истории чата выполнена: %v", s.player.Done())
	}

	if r := s.command("!next"); !strings.Contains(r, "Дальше") {
		t.Fatalf("ответ на !next: %q", r)
	}
	if r := s.command("  !Пауза  "); !strings.Contains(r, "Пауза") {
		t.Fatalf("ответ на !пауза: %q", r)
	}
	if r := s.command("!prev"); !strings.Contains(r, "Назад") {
		t.Fatalf("ответ на !prev: %q", r)
	}
	done := s.player.Done()
	if len(done) != 3 || done[0].Cmd != control.Next || done[1].Cmd != control.Pause || done[2].Cmd != control.Prev || done[0].From != control.API {
		t.Fatalf("плеер получил: %+v", done)
	}

	s.command("!play")
	s.command("!play Кино   Группа крови")
	s.srv.Say(3, "Анна", "!алиса какая погода") // ответит сама Алиса голосом: в чат ничего
	wait(t, "три фразы Алисе", func() bool { return len(s.askedPhrases()) == 3 })
	if got := s.askedPhrases(); got[0] != "играй" || got[1] != "включи Кино Группа крови" || got[2] != "какая погода" {
		t.Fatalf("Алисе сказано: %q", got)
	}

	if r := s.command("!vol 30"); !strings.Contains(r, "30") || s.c.Snapshot().Volume != 30 {
		t.Fatalf("громкость: ответ %q, состояние %d", r, s.c.Snapshot().Volume)
	}
	if r := s.command("!vol +"); !strings.Contains(r, "40") {
		t.Fatalf("громкость +: %q", r)
	}
	if r := s.command("!vol 500"); !strings.Contains(r, "от 0 до 100") || s.c.Snapshot().Volume != 40 {
		t.Fatalf("громкость 500: %q", r)
	}
	if r := s.command("!help"); !strings.Contains(r, "!play") || !strings.Contains(r, "!алиса") {
		t.Fatalf("справка: %q", r)
	}

	// не команды: обычный текст, знаки, чужой префикс
	s.srv.Say(3, "Анна", "привет всем")
	s.srv.Say(3, "Анна", "!!!")
	s.srv.Say(3, "Анна", "/next")
	quiet(t, s.srv.Chats, 150*time.Millisecond, "колонка ответила не на команду")

	// фразы Алисе выключены: !алиса и !play с запросом отказывают, !play без запроса работает
	s.c.Update(func(set *Settings) { set.Alice = false })
	if r := s.command("!алиса закажи такси"); !strings.Contains(r, "выключены") {
		t.Fatalf("ответ при выключенных фразах: %q", r)
	}
	if r := s.command("!play свет в спальне"); !strings.Contains(r, "выключены") {
		t.Fatalf("ответ на !play с запросом: %q", r)
	}
	s.command("!play")
	if got := s.askedPhrases(); len(got) != 4 || got[3] != "играй" {
		t.Fatalf("Алисе сказано: %q", got)
	}

	// команды чата выключены совсем
	s.c.Update(func(set *Settings) { set.Chat = false })
	s.srv.Say(3, "Анна", "!next")
	quiet(t, s.srv.Chats, 150*time.Millisecond, "команда выполнена при выключенном чате")
	if s.player.Count() != 3 {
		t.Fatalf("плеер получил лишнее: %+v", s.player.Done())
	}
	if st := s.c.Snapshot(); st.Commands != 13 {
		t.Fatalf("команд выполнено %d", st.Commands)
	}
}

// Частые команды: после запаса колонка один раз предупреждает и перестаёт выполнять.
func TestChatFlood(t *testing.T) {
	s := newStand(t)
	s.c.bucket = newBucket(bucketSize)
	s.join()
	for i := 0; i < bucketSize+5; i++ {
		s.srv.Say(3, "Анна", "!next")
	}
	wait(t, "запас команд выполнен", func() bool { return s.player.Count() == bucketSize })
	var warned int
	deadline := time.After(400 * time.Millisecond)
loop:
	for {
		select {
		case r := <-s.srv.Chats:
			if strings.Contains(r, "Слишком много") {
				warned++
			}
		case <-deadline:
			break loop
		}
	}
	if warned != 1 || s.player.Count() != bucketSize {
		t.Fatalf("предупреждений %d, команд выполнено %d", warned, s.player.Count())
	}
}

// Обрыв связи: колонка входит снова, а поток движка это время ждёт в смесителе и не перескакивает на динамики.
func TestReconnectKeepsStream(t *testing.T) {
	s := newStand(t)
	s.join()
	out, err := s.c.Open(audio.Mono48)
	if err != nil {
		t.Fatal(err)
	}
	failed := make(chan error, 1)
	stop := make(chan struct{})
	go func() { // движок пишет музыку без остановки
		chunk := tone(audio.Mono48, 20*time.Millisecond)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := out.Write(chunk); err != nil {
				failed <- err
				return
			}
		}
	}()
	defer close(stop)
	recv(t, s.srv.Audio, "звук до обрыва")

	s.srv.Drop()
	wait(t, "повторный вход", func() bool { return len(s.srv.Joins()) == 2 && s.c.Snapshot().Phase == phaseJoined })
	for len(s.srv.Audio) > 0 {
		<-s.srv.Audio
	}
	recv(t, s.srv.Audio, "звук после повторного входа")
	select {
	case err := <-failed:
		t.Fatalf("поток снят со смесителя на коротком обрыве: %v", err)
	default:
	}
	quiet(t, s.srv.Chats, 100*time.Millisecond, "приветствие повторилось после переподключения")

	// сервер закрыл комнату: окончательный отказ, поток уходит на следующий выход
	s.srv.RefuseJoin("room_not_found")
	s.srv.Drop()
	s.waitPhase(phaseFailed)
	if err := recv(t, failed, "поток снят после отказа"); err == nil {
		t.Fatal("поток остался в смесителе")
	}
	if st := s.c.Snapshot(); !strings.Contains(st.Error, "комнаты нет") || s.c.Available() {
		t.Fatalf("после отказа: %+v, доступен %v", st, s.c.Available())
	}
	joins := len(s.srv.Joins())
	time.Sleep(200 * time.Millisecond)
	if len(s.srv.Joins()) != joins {
		t.Fatal("после окончательного отказа колонка продолжает стучаться")
	}

	// человек даёт новую ссылку — колонка входит
	s.srv.RefuseJoin("")
	if _, err := s.c.Update(func(set *Settings) { set.Link = s.srv.Link("room2") }); err != nil {
		t.Fatal(err)
	}
	s.waitPhase(phaseJoined)
	recv(t, s.srv.Chats, "приветствие в новой комнате")
}

// Связи нет дольше срока ожидания: поток уходит на следующий выход, не дожидаясь возвращения.
func TestLongOutageReleasesStream(t *testing.T) {
	shorten(t, &holdFor, 150*time.Millisecond)
	s := newStand(t)
	s.join()
	out, _ := s.c.Open(audio.Mono48)
	out.Write(tone(audio.Mono48, 40*time.Millisecond))
	s.srv.RefuseJoin("room_offline") // вход не окончательно отвергнут: колонка будет повторять
	s.srv.Drop()
	s.waitPhase(phaseWaiting)
	if !s.c.Available() {
		t.Fatal("сразу после обрыва выход должен ждать возвращения")
	}
	wait(t, "поток снят по сроку", func() bool {
		_, err := out.Write(tone(audio.Mono48, 20*time.Millisecond))
		return err != nil
	})
	if s.c.Available() {
		t.Fatal("после срока выход всё ещё доступен")
	}
}

// Вывод звука выключают и включают на ходу; «выйти» отпускает потоки и закрывает соединение.
func TestAudioSwitchAndLeave(t *testing.T) {
	s := newStand(t)
	s.join()
	out, _ := s.c.Open(audio.Stereo48)
	s.c.Update(func(set *Settings) { set.Audio = false })
	if _, err := out.Write(tone(audio.Stereo48, 20*time.Millisecond)); err == nil || s.c.Available() {
		t.Fatal("вывод выключен, а поток остался в конференции")
	}
	s.c.Update(func(set *Settings) { set.Audio = true })
	wait(t, "сообщение «выход появился»", func() bool { return s.arrived() == 2 })

	out, _ = s.c.Open(audio.Stereo48)
	s.c.Update(func(set *Settings) { set.Connect = false })
	s.waitPhase(phaseOff)
	wait(t, "соединение закрыто", func() bool { return s.srv.Clients() == 0 })
	if _, err := out.Write(tone(audio.Stereo48, 20*time.Millisecond)); err == nil {
		t.Fatal("после выхода из комнаты поток остался в смесителе")
	}

	// настройки пережили перезапуск: файл состояния
	set, err := loadSettings(s.c.path, "Колонка")
	if err != nil || set.Connect || !set.Audio || set.Link != s.srv.Link(roomCode) {
		t.Fatalf("файл состояния: %+v, %v", set, err)
	}
	if _, err := s.c.Update(func(set *Settings) { set.Link, set.Connect = "ftp://нет", true }); err == nil {
		t.Fatal("негодная ссылка принята")
	}
	if s.c.Snapshot().Connect {
		t.Fatal("негодная ссылка изменила настройки")
	}
}

func TestSettingsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "meetup.json")
	set, err := loadSettings(path, "Колонка")
	if err != nil || set.Name != "Колонка" || !set.Audio || !set.Chat || !set.Alice || set.Volume != 50 || set.Connect {
		t.Fatalf("умолчания: %+v, %v", set, err)
	}
	set.Name, set.Volume, set.Link = "  Станция \n Мини ", 250, " https://x/conference.html?room=1 "
	set.normalize("Колонка")
	if set.Name != "Станция Мини" || set.Volume != 100 || set.Link != "https://x/conference.html?room=1" {
		t.Fatalf("приведение: %+v", set)
	}
	if err := saveSettings(path, set); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(path, "Колонка")
	if err != nil || !reflect.DeepEqual(got, set) {
		t.Fatalf("прочитано %+v, %v", got, err)
	}
	os.WriteFile(path, []byte("{не json"), 0o600)
	if got, err := loadSettings(path, "Колонка"); err == nil || got.Volume != 50 {
		t.Fatalf("испорченный файл: %+v, %v", got, err)
	}
}

func TestConfigValidate(t *testing.T) {
	if err := defaults().validate(); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Config){
		"listen":     func(c *Config) { c.Listen = "6702" },
		"name":       func(c *Config) { c.Name = "" },
		"prefix":     func(c *Config) { c.Prefix = "бот!" },
		"bitrate":    func(c *Config) { c.Bitrate = 8000 },
		"complexity": func(c *Config) { c.Complexity = 11 },
	} {
		c := defaults()
		edit(c)
		if err := c.validate(); err == nil || !strings.Contains(err.Error(), "meetup."+name) {
			t.Errorf("%s: %v", name, err)
		}
	}
	c := defaults()
	c.Listen, c.Prefix = "", ""
	if err := c.validate(); err != nil {
		t.Fatalf("без страницы и без команд: %v", err)
	}
}
