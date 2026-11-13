package control

import (
	"errors"
	"testing"
)

// fakeRemote — пульт музыки платформы: помнит, что ему передали.
type fakeRemote struct {
	got []Cmd
	err error
}

func (r *fakeRemote) Command(c Cmd) error {
	if r.err != nil {
		return r.err
	}
	r.got = append(r.got, c)
	return nil
}

// Command — один вызов для источника: играет плеер движка — команду выполняет он, и платформа о ней не узнаёт.
func TestCommandGoesToEnginePlayerFirst(t *testing.T) {
	r := newRig(ready(true))
	rem := &fakeRemote{}
	r.h.Remote = rem
	by, err := r.h.Command(Toggle, Headphones)
	if err != nil || by != "spotify" {
		t.Fatalf("выполнил %q, ошибка %v; ждали spotify", by, err)
	}
	if c, _ := r.p.last(); c != Pause {
		t.Fatalf("плеер получил %q, ждали паузу", c)
	}
	if len(rem.got) != 0 {
		t.Fatalf("платформе команда уходить не должна: %v", rem.got)
	}
}

// Играет музыка платформы (или ничего): команда источника, о котором платформа не знает, уходит ей через пульт, как нажатие кнопки на колонке.
// Событие объявляется с тем, что это нажатие значило, и с отметкой, что показывает его сама прошивка.
func TestCommandFallsBackToPlatformMusic(t *testing.T) {
	for _, from := range []Origin{Headphones, API} {
		r := newRig(ready(false)) // плеер движка готов, но молчит и не на паузе
		rem := &fakeRemote{}
		r.h.Remote = rem
		r.plat.setYandex(true)
		var rec recorder
		r.h.Subscribe(rec.on)
		var logged []string
		r.h.OnHandled = func(c Cmd, o Origin, player string) { logged = append(logged, player+":"+string(c)) }
		for _, c := range []Cmd{Toggle, Next, Prev} {
			by, err := r.h.Command(c, from)
			if err != nil || by != OwnerPlatform {
				t.Fatalf("%s от %s: выполнил %q, ошибка %v; ждали музыку платформы", c, from, by, err)
			}
		}
		if len(rem.got) != 3 || rem.got[0] != Toggle || rem.got[1] != Next || rem.got[2] != Prev {
			t.Fatalf("%s: платформа получила %v", from, rem.got)
		}
		if r.p.count() != 0 {
			t.Fatalf("%s: плеер движка получил чужую команду", from)
		}
		want := []Event{
			{Cmd: Pause, From: from, Player: OwnerPlatform, PlatformDisplay: true, PlatformRing: true},
			{Cmd: Next, From: from, Player: OwnerPlatform, PlatformDisplay: true, PlatformRing: true},
			{Cmd: Prev, From: from, Player: OwnerPlatform, PlatformDisplay: true, PlatformRing: true},
		}
		if len(rec.got) != len(want) {
			t.Fatalf("%s: события %+v", from, rec.got)
		}
		for i := range want {
			if rec.got[i] != want[i] {
				t.Errorf("%s: событие %d: %+v, ждали %+v", from, i, rec.got[i], want[i])
			}
		}
		if len(logged) != 3 || logged[0] != OwnerPlatform+":pause" {
			t.Fatalf("%s: журнал %v", from, logged)
		}
		// музыка платформы стоит: то же нажатие значит «продолжить»
		r.plat.setYandex(false)
		r.h.Command(Toggle, from)
		if last := rec.got[len(rec.got)-1]; last.Cmd != Resume {
			t.Fatalf("%s: событие %+v, ждали «продолжить»", from, last)
		}
	}
}

// О кнопке колонки, голосе, директиве облака и собственной команде платформа знает сама: второй раз ей команду не передают.
func TestCommandDoesNotEchoWhatPlatformAlreadySees(t *testing.T) {
	for _, from := range []Origin{Speaker, Voice, FastVoice, Cloud, Virtual} {
		r := newRig(State{}) // плеер движка не готов
		rem := &fakeRemote{}
		r.h.Remote = rem
		r.plat.setYandex(true)
		by, err := r.h.Command(Next, from)
		if by != "" || err != nil || len(rem.got) != 0 {
			t.Fatalf("%s: выполнил %q, ошибка %v, платформе ушло %v; ждали ничего", from, by, err, rem.got)
		}
	}
}

// Нет пульта — команда остаётся невыполненной (источник поступает по-старому); пульт не смог — ошибка, события нет.
func TestCommandWithoutRemoteOrWithFailure(t *testing.T) {
	r := newRig(State{})
	if by, err := r.h.Command(Pause, Headphones); by != "" || err != nil {
		t.Fatalf("без пульта: %q %v", by, err)
	}
	var rec recorder
	r.h.Subscribe(rec.on)
	r.h.Remote = &fakeRemote{err: errors.New("aliced не отвечает")}
	if by, err := r.h.Command(Pause, Headphones); by != "" || err == nil {
		t.Fatalf("пульт сломан: %q %v", by, err)
	}
	if len(rec.got) != 0 {
		t.Fatalf("о невыполненной команде объявлять нельзя: %+v", rec.got)
	}
	var nilHub *Hub
	if by, err := nilHub.Command(Pause, Headphones); by != "" || err != nil {
		t.Fatalf("пустое ядро: %q %v", by, err)
	}
}

// Отметки «покажет прошивка» у событий плеера движка: таблица из visuals.go.
func TestEventsSayWhoDrawsTheAnimation(t *testing.T) {
	type key struct {
		c    Cmd
		from Origin
	}
	display := map[key]bool{
		{Pause, Speaker}: true, {Pause, FastVoice}: true, {Next, FastVoice}: true, {Resume, Cloud}: true, {Prev, Virtual}: true,
		{Next, Speaker}: false, {Resume, Speaker}: false, {Pause, Voice}: false, {Next, Voice}: false,
		{Pause, Headphones}: false, {Next, App}: false, {Pause, API}: false, {Pause, Arbiter}: false,
	}
	ring := map[key]bool{
		{Pause, FastVoice}: true, {Next, Cloud}: true, {Prev, Virtual}: true,
		{Pause, Speaker}: false, {Pause, Voice}: false, {Pause, Headphones}: false, {Next, App}: false, {Next, API}: false,
	}
	for k, want := range display {
		if got := platformDisplay(Event{Cmd: k.c, From: k.from, Player: "spotify"}); got != want {
			t.Errorf("дисплей: %s от %s: %v, ждали %v", k.c, k.from, got, want)
		}
	}
	for k, want := range ring {
		if got := platformRing(Event{Cmd: k.c, From: k.from, Player: "spotify"}); got != want {
			t.Errorf("кольцо: %s от %s: %v, ждали %v", k.c, k.from, got, want)
		}
	}
	// событие, которое Hub объявил сам, несёт те же отметки
	r := newRig(ready(true))
	var rec recorder
	r.h.Subscribe(rec.on)
	r.h.Do(Toggle, Speaker)
	if len(rec.got) != 1 || !rec.got[0].PlatformDisplay || rec.got[0].PlatformRing {
		t.Fatalf("пауза кнопкой колонки: %+v; ждали дисплей — прошивка, кольцо — нет", rec.got)
	}
}

func TestPlatformSees(t *testing.T) {
	for o, want := range map[Origin]bool{Speaker: true, Voice: true, FastVoice: true, Cloud: true, Virtual: true,
		Headphones: false, API: false, App: false, Arbiter: false} {
		if o.PlatformSees() != want {
			t.Errorf("%s: PlatformSees = %v, ждали %v", o, !want, want)
		}
	}
}
