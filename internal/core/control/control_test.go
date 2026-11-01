package control

import "testing"

type stub struct {
	take bool
	got  []Cmd
	from []Origin
}

func (s *stub) Handle(c Cmd, from Origin) bool {
	s.got = append(s.got, c)
	s.from = append(s.from, from)
	return s.take
}

// Команду получает первый плеер, который её выполнил; остальные её уже не видят.
func TestDoStopsAtTheFirstPlayerThatHandledIt(t *testing.T) {
	skip, first, second := &stub{}, &stub{take: true}, &stub{take: true}
	var logged []string
	h := &Hub{OnHandled: func(c Cmd, from Origin, player string) {
		logged = append(logged, player+":"+string(c)+":"+string(from))
	}}
	h.Add("skip", skip)
	h.Add("first", first)
	h.Add("second", second)
	if !h.Do(Next, Headphones) {
		t.Fatal("команду никто не взял")
	}
	if len(skip.got) != 1 || len(first.got) != 1 || first.got[0] != Next || first.from[0] != Headphones {
		t.Fatalf("skip %v, first %v %v", skip.got, first.got, first.from)
	}
	if len(second.got) != 0 {
		t.Fatalf("команда дошла до второго плеера: %v", second.got)
	}
	if len(logged) != 1 || logged[0] != "first:next:headphones" {
		t.Fatalf("журнал: %v", logged)
	}
}

// Никто не взял — false, и OnHandled молчит: источник сам решит, что делать.
func TestDoReportsWhenNobodyTookTheCommand(t *testing.T) {
	called := false
	h := &Hub{OnHandled: func(Cmd, Origin, string) { called = true }}
	h.Add("a", &stub{})
	if h.Do(Pause, Speaker) {
		t.Fatal("команду взял тот, кто отказался")
	}
	if called {
		t.Fatal("OnHandled вызван, хотя команду никто не выполнил")
	}
	if (&Hub{}).Do(Pause, Speaker) {
		t.Fatal("пустой Hub взял команду")
	}
}

// Источник, у которого Hub'а нет (компонент собран без ядра, тесты), может вызывать Do без проверки.
func TestNilHubTakesNothing(t *testing.T) {
	var h *Hub
	if h.Do(Toggle, Headphones) {
		t.Fatal("пустой указатель принял команду")
	}
}

func TestLen(t *testing.T) {
	var h Hub
	if h.Len() != 0 {
		t.Fatal("новый Hub не пуст")
	}
	h.Add("a", &stub{})
	if h.Len() != 1 {
		t.Fatalf("плееров %d, ждали 1", h.Len())
	}
}

func TestOnlySpeakerIsSeenByThePlatform(t *testing.T) {
	if !Speaker.PlatformReacts() || Headphones.PlatformReacts() {
		t.Fatal("платформа видит кнопки колонки, но не наушников")
	}
}

func TestNames(t *testing.T) {
	for c, want := range map[Cmd]string{Pause: "пауза", Resume: "продолжить", Toggle: "пауза/продолжить", Next: "следующий трек", Prev: "предыдущий трек"} {
		if c.String() != want {
			t.Errorf("%q: %q, ждали %q", string(c), c.String(), want)
		}
	}
}
