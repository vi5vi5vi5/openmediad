package control

import (
	"fmt"
	"testing"
)

// Таблица сценариев: в какой обстановке какая команда кому достаётся. Это и есть правила ядра «чей звук», записанные в одном месте: меняя
// правило, меняют строку здесь, а не ищут по тестам. Ответ не зависит от того, откуда пришла команда (кнопка, наушники, голос, облако), поэтому
// каждая строка проверяется для всех источников. От источника зависит только, готовить ли автомат к запуску очереди Яндекса (ниже).

// scenario — обстановка: состояние плеера движка, играет ли Яндекс и кто играл последним.
type scenario struct {
	name   string
	st     State
	yandex bool
	owner  string
	// want — что плеер должен выполнить на команду; пусто — команда не его (достаётся платформе или Алисе)
	want map[Cmd]Cmd
}

var scenarios = []scenario{
	{
		name: "плеер играет, Яндекс молчит",
		st:   ready(true), owner: "spotify",
		want: map[Cmd]Cmd{Pause: Pause, Toggle: Pause, Next: Next, Prev: Prev},
	},
	{
		name: "плеер на паузе, сессия его, последним играл он",
		st:   paused(), owner: "spotify",
		want: map[Cmd]Cmd{Resume: Resume, Toggle: Resume, Next: Next, Prev: Prev},
	},
	{
		name: "плеер на паузе, сессия его, но Яндекс играл позже",
		st:   paused(), owner: OwnerPlatform,
		want: map[Cmd]Cmd{},
	},
	{
		name: "плеер на паузе, но устройство не выбрано в приложении (сессия не его)",
		st:   State{Ready: true, Paused: true}, owner: "spotify",
		want: map[Cmd]Cmd{},
	},
	{
		name: "плеер ничего не играет и продолжать нечего",
		st:   State{Ready: true}, owner: "",
		want: map[Cmd]Cmd{},
	},
	{
		name: "плеер и Яндекс играют вместе (до уступки звука)",
		st:   ready(true), yandex: true, owner: "spotify",
		want: map[Cmd]Cmd{},
	},
	{
		name: "плеер на паузе, Яндекс играет",
		st:   paused(), yandex: true, owner: "spotify",
		want: map[Cmd]Cmd{},
	},
	{
		name: "плеер не готов (нет связи или входа)",
		st:   State{Playing: true, Sounding: true}, owner: "spotify",
		want: map[Cmd]Cmd{},
	},
}

var allCmds = []Cmd{Pause, Resume, Toggle, Next, Prev}

func TestScenarioMatrix(t *testing.T) {
	for _, s := range scenarios {
		for _, from := range []Origin{Speaker, Headphones, Voice, FastVoice, Cloud} {
			for _, cmd := range allCmds {
				t.Run(fmt.Sprintf("%s/%s/%s", s.name, from, cmd), func(t *testing.T) {
					r := newRig(s.st)
					r.plat.setYandex(s.yandex)
					r.h.SetOwner(s.owner)
					var events []Event
					r.h.Subscribe(func(e Event) { events = append(events, e) })

					want, ours := s.want[cmd]
					handled := r.h.Do(cmd, from)
					if handled != ours {
						t.Fatalf("взято: %v, ждали %v", handled, ours)
					}
					if !ours {
						if r.p.count() != 0 || len(events) != 0 || r.plat.armedN() != 0 {
							t.Fatalf("команда не взята, а плееру велели %v, событий %d, очередь Яндекса подготовлена %d раз", r.p.did, len(events), r.plat.armedN())
						}
						return
					}
					if got := r.p.did; len(got) != 1 || got[0] != want || r.p.froms[0] != from {
						t.Fatalf("плееру велели %v от %v, ждали %q от %v", r.p.did, r.p.froms, want, from)
					}
					if len(events) != 1 || events[0] != (Event{Cmd: want, From: from, Player: "spotify"}) {
						t.Fatalf("события %+v", events)
					}
					// очередь Яндекса готовят, только когда платформа ответит на действие запросом облаку: кнопка колонки, голос, короткая команда
					wantArmed := 0
					if want != Pause && from.PlatformReacts() {
						wantArmed = 1
					}
					if got := r.plat.armedN(); got != wantArmed {
						t.Fatalf("очередь Яндекса подготовлена %d раз, ждали %d", got, wantArmed)
					}
				})
			}
		}
	}
}

// Команда платформы виртуальному плееру адресована именно ему, поэтому исполняется при любом звуке, лишь бы плеер был готов.
func TestScenarioMatrixForPlatformCommands(t *testing.T) {
	for _, s := range scenarios {
		for cmd, want := range map[Cmd]Cmd{Pause: Pause, Resume: Resume} {
			t.Run(fmt.Sprintf("%s/%s", s.name, cmd), func(t *testing.T) {
				r := newRig(s.st)
				r.plat.setYandex(s.yandex)
				r.h.SetOwner(s.owner)
				handled := r.h.Do(cmd, Virtual)
				if handled != s.st.Ready {
					t.Fatalf("взято: %v, готов: %v", handled, s.st.Ready)
				}
				if s.st.Ready && (len(r.p.did) != 1 || r.p.did[0] != want) {
					t.Fatalf("плееру велели %v, ждали %q", r.p.did, want)
				}
				if r.plat.armedN() != 0 {
					t.Fatal("на свою же команду платформа очередь Яндекса не запрашивает")
				}
			})
		}
	}
}

// Настройки компонента: плеер слушается не всех источников. Остальные источники при этом работают, и обстановка та же.
func TestScenarioMatrixWhenThePlayerRefusesASource(t *testing.T) {
	groups := map[string][]Origin{
		"голос выключен":   {Voice, FastVoice, Cloud},
		"кнопки выключены": {Speaker, Headphones},
	}
	for name, refused := range groups {
		for _, from := range []Origin{Speaker, Headphones, Voice, FastVoice, Cloud} {
			isRefused := false
			for _, o := range refused {
				isRefused = isRefused || o == from
			}
			t.Run(fmt.Sprintf("%s/%s", name, from), func(t *testing.T) {
				r := newRig(ready(true))
				r.p.refuse = map[Origin]bool{}
				for _, o := range refused {
					r.p.refuse[o] = true
				}
				if handled := r.h.Do(Pause, from); handled == isRefused {
					t.Fatalf("взято: %v, отказано источнику: %v", handled, isRefused)
				}
			})
		}
	}
}

// Два плеера: команду получает тот, чей сейчас звук; второй, стоящий на паузе, её не видит.
func TestScenarioTwoPlayers(t *testing.T) {
	r := newRig(State{Ready: true})
	other := &fakePlayer{st: ready(true)}
	r.h.Add("other", other)
	r.h.SetOwner("other")
	if !r.h.Do(Next, Speaker) {
		t.Fatal("команда не взята")
	}
	if other.count() != 1 || r.p.count() != 0 {
		t.Fatalf("команду получили: второй %d, первый %d", other.count(), r.p.count())
	}
	// первый заиграл (а второй замолчал): теперь команда его
	r.p.set(ready(true))
	other.set(paused())
	r.h.SetOwner("spotify")
	if !r.h.Do(Pause, Headphones) || r.p.count() != 1 {
		t.Fatalf("пауза не дошла до игравшего плеера: %v", r.p.did)
	}
}
