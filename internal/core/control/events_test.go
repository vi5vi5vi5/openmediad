package control

import (
	"testing"
	"time"
)

// recorder собирает события подписчика.
type recorder struct{ got []Event }

func (r *recorder) on(e Event) { r.got = append(r.got, e) }

func (r *recorder) cmds() []Cmd {
	out := make([]Cmd, len(r.got))
	for i, e := range r.got {
		out[i] = e.Cmd
	}
	return out
}

func playingTrack(track string) State {
	s := ready(true)
	s.Track = track
	return s
}

func pausedTrack(track string) State {
	s := paused()
	s.Track = track
	return s
}

// Soloist после запуска перед «играет» ненадолго буферизует (ни «играет», ни «на паузе»): продолжение должно заметиться и так.
func TestResumeThroughBufferingIsSeen(t *testing.T) {
	a := newArbRig(pausedTrack("t1"))
	var rec recorder
	a.h.Subscribe(rec.on)
	a.step()
	a.p.set(State{Ready: true, Track: "t1"}) // буферизует
	a.step()
	a.step()
	a.p.set(playingTrack("t1"))
	a.step()
	a.p.set(State{Ready: true, Track: "t1"}) // и ещё раз буферизует, а потом пауза
	a.step()
	a.advance(5 * time.Second)
	a.p.set(pausedTrack("t1"))
	a.step()
	if got := rec.cmds(); len(got) != 2 || got[0] != Resume || got[1] != Pause {
		t.Fatalf("события %v, ждали продолжить и паузу", got)
	}
}

// Устройство не выбрано в приложении (сессия не за ним), но статус «на паузе» тот же: пауза и продолжение всё равно видны подсветке.
func TestPauseAndResumeAreSeenEvenWhenTheSessionIsNotOurs(t *testing.T) {
	a := newArbRig(playingTrack("t1"))
	var rec recorder
	a.h.Subscribe(rec.on)
	a.step()
	a.p.set(State{Ready: true, Paused: true, Track: "t1"}) // Resumable нет
	a.step()
	a.p.set(playingTrack("t1"))
	a.step()
	if got := rec.cmds(); len(got) != 2 || got[0] != Pause || got[1] != Resume {
		t.Fatalf("события %v", got)
	}
}

// Команда, выполненная через Hub, объявляется сразу: с тем, что сделано (а не что просили) и откуда пришло.
func TestHubAnnouncesWhatItExecuted(t *testing.T) {
	r := newRig(ready(true))
	var rec recorder
	r.h.Subscribe(rec.on)
	r.h.Do(Toggle, Headphones) // играет: переключатель — это пауза
	r.p.set(paused())
	r.h.SetOwner("spotify")
	r.h.Do(Toggle, Speaker)
	r.p.set(ready(true))
	r.h.Do(Next, Voice)
	r.h.Do(Prev, Headphones)
	want := []Event{
		{Pause, Headphones, "spotify"}, {Resume, Speaker, "spotify"}, {Next, Voice, "spotify"}, {Prev, Headphones, "spotify"},
	}
	if len(rec.got) != len(want) {
		t.Fatalf("событий %d: %+v", len(rec.got), rec.got)
	}
	for i, e := range want {
		if rec.got[i] != e {
			t.Errorf("событие %d: %+v, ждали %+v", i, rec.got[i], e)
		}
	}
}

func TestNothingIsAnnouncedForRefusedOrFailedCommands(t *testing.T) {
	r := newRig(paused())
	var rec recorder
	r.h.Subscribe(rec.on)
	r.h.Do(Pause, Voice) // на паузе: не наша
	r.p.set(ready(true))
	r.p.err = errNoLink
	r.h.Do(Next, Voice) // не вышло
	if len(rec.got) != 0 {
		t.Fatalf("объявлено лишнее: %+v", rec.got)
	}
}

var errNoLink = &linkError{}

type linkError struct{}

func (*linkError) Error() string { return "нет связи" }

func TestUnsubscribe(t *testing.T) {
	r := newRig(ready(true))
	var rec recorder
	stop := r.h.Subscribe(rec.on)
	r.h.Do(Pause, Voice)
	stop()
	r.p.set(ready(true))
	r.advance(10 * time.Second)
	r.h.Do(Pause, Voice)
	if len(rec.got) != 1 {
		t.Fatalf("после отписки пришло ещё: %+v", rec.got)
	}
}

// Пауза и «продолжить» из приложения Spotify: через Hub ничего не шло, но состояние плеера изменилось, и ядро объявляет это само.
func TestPauseAndResumeFromTheAppAreAnnounced(t *testing.T) {
	a := newArbRig(playingTrack("t1"))
	var rec recorder
	a.h.Subscribe(rec.on)
	a.step() // первый взгляд: сравнивать не с чем
	a.p.set(pausedTrack("t1"))
	a.step()
	a.step()
	a.p.set(playingTrack("t1"))
	a.step()
	if got := rec.cmds(); len(got) != 2 || got[0] != Pause || got[1] != Resume {
		t.Fatalf("события %v, ждали пауза и продолжить", got)
	}
	if rec.got[0].From != App || rec.got[0].Player != "spotify" {
		t.Fatalf("источник %+v", rec.got[0])
	}
}

// Начало новой музыки (из остановленного состояния) — не «продолжить»: подсветка на неё отвечает своей вспышкой.
func TestStartingFromStoppedIsNotAResume(t *testing.T) {
	a := newArbRig(State{Ready: true})
	var rec recorder
	a.h.Subscribe(rec.on)
	a.step()
	a.p.set(playingTrack("t1"))
	a.step()
	if len(rec.got) != 0 {
		t.Fatalf("объявлено %+v", rec.got)
	}
	// и обрыв связи — не пауза
	a.p.set(State{})
	a.step()
	if len(rec.got) != 0 {
		t.Fatalf("конец сессии объявлен паузой: %+v", rec.got)
	}
}

// Паузу, которую выполнили через Hub, ядро уже объявило: когда плеер потом сам поменяет состояние, второй раз она не объявляется.
func TestCommandedChangeIsNotAnnouncedTwice(t *testing.T) {
	a := newArbRig(playingTrack("t1"))
	var rec recorder
	a.h.Subscribe(rec.on)
	a.step()
	a.h.Do(Pause, Headphones)
	a.p.set(pausedTrack("t1"))
	a.advance(300 * time.Millisecond)
	a.step()
	a.h.SetOwner("spotify")
	a.h.Do(Resume, Headphones)
	a.p.set(playingTrack("t1"))
	a.advance(300 * time.Millisecond)
	a.step()
	a.p.set(playingTrack("t2")) // и перемотка после команды «дальше»
	a.h.Do(Next, Headphones)
	a.advance(500 * time.Millisecond)
	a.step()
	if got := rec.cmds(); len(got) != 3 || got[0] != Pause || got[1] != Resume || got[2] != Next {
		t.Fatalf("события %v: каждое изменение должно объявиться один раз", got)
	}
	// через много секунд то же изменение, пришедшее само, уже новое событие
	a.advance(10 * time.Second)
	a.p.set(pausedTrack("t2"))
	a.step()
	a.p.set(playingTrack("t2"))
	a.step()
	if got := rec.cmds(); len(got) != 5 || got[3] != Pause || got[4] != Resume {
		t.Fatalf("события %v", got)
	}
}

// Перемотка в приложении: смена трека — «дальше», а возврат к треку перед прошлым — «назад». Доигранный трек и первый трек — не перемотка.
func TestSkipsFromTheAppAreAnnouncedWithDirection(t *testing.T) {
	a := newArbRig(playingTrack("t1"))
	var rec recorder
	a.h.Subscribe(rec.on)
	a.step()
	a.p.set(playingTrack("t2"))
	a.step()
	a.advance(5 * time.Second) // перемотки — отдельные события, а не повтор одной
	a.p.set(playingTrack("t3"))
	a.step()
	a.advance(5 * time.Second)
	a.p.set(playingTrack("t2")) // назад
	a.step()
	if got := rec.cmds(); len(got) != 3 || got[0] != Next || got[1] != Next || got[2] != Prev {
		t.Fatalf("события %v, ждали дальше, дальше, назад", got)
	}
}

// Между треками Soloist ненадолго буферизует: трек уже новый, а плеер ещё не заиграл. Перемотка замечается, когда состояние стало устойчивым.
func TestSkipSeenThroughBuffering(t *testing.T) {
	a := newArbRig(playingTrack("t1"))
	var rec recorder
	a.h.Subscribe(rec.on)
	a.step()
	a.p.set(State{Ready: true, Track: "t2"}) // буферизует
	a.step()
	a.p.set(playingTrack("t2"))
	a.step()
	a.step()
	if got := rec.cmds(); len(got) != 1 || got[0] != Next {
		t.Fatalf("события %v, ждали одну перемотку", got)
	}
}

func TestNaturalEndOfATrackIsNotASkip(t *testing.T) {
	a := newArbRig(playingTrack("t1"))
	var rec recorder
	a.h.Subscribe(rec.on)
	a.step()
	end := playingTrack("t1")
	end.NearEnd = true
	a.p.set(end)
	a.step()
	a.p.set(playingTrack("t2")) // трек доиграл сам
	a.step()
	if len(rec.got) != 0 {
		t.Fatalf("конец трека объявлен перемоткой: %+v", rec.got)
	}
	a.advance(5 * time.Second)
	a.p.set(playingTrack("t3")) // а это уже перемотка посреди трека
	a.step()
	if got := rec.cmds(); len(got) != 1 || got[0] != Next {
		t.Fatalf("события %v", got)
	}
}

// Пауза, которой плеер уступил звук Яндексу, — не пауза «из приложения»: рядом заиграл Яндекс со своей подсветкой.
func TestPausedByTheArbiterIsNotAnnounced(t *testing.T) {
	a := newArbRig(playingTrack("t1"))
	var rec recorder
	a.h.Subscribe(rec.on)
	a.step()
	a.plat.setYandex(true)
	a.step() // арбитр ставит плеер на паузу
	a.p.set(pausedTrack("t1"))
	a.step()
	if len(rec.got) != 0 {
		t.Fatalf("уступка звука объявлена событием: %+v", rec.got)
	}
}

// События видны и без платформы (в тестах и у сборок без Яндекс.Музыки).
func TestEventsWorkWithoutAPlatform(t *testing.T) {
	r := newRig(playingTrack("t1"))
	r.h.Platform = nil
	arb := &arbRig{rig: r, prev: map[string]bool{}}
	var rec recorder
	r.h.Subscribe(rec.on)
	arb.step()
	r.p.set(pausedTrack("t1"))
	arb.step()
	if got := rec.cmds(); len(got) != 1 || got[0] != Pause {
		t.Fatalf("события %v", got)
	}
}
