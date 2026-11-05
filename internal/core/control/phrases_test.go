package control

import "testing"

func TestPhraseGrammar(t *testing.T) {
	for text, want := range map[string]Cmd{
		"Пауза":  Pause,
		"пауза.": Pause,
		"Поставь на паузу, пожалуйста!": Pause,
		"Останови музыку":               Pause,
		"Продолжи":                      Resume,
		"Сними с паузы":                 Resume,
		"Играй":                         Resume,
		"Дальше":                        Next,
		"Следующая песня":               Next,
		"Включи следующий трек":         Next,
		"Пропусти":                      Next,
		"Назад":                         Prev,
		"Предыдущий трек":               Prev,
		"Включи предыдущую песню":       Prev,
		"  ДАЛЬШЕ  ":                    Next,
		"Вернись":                       Prev,
		"Алиса, ёлка дальше":            "", // не из грамматики
	} {
		got, ok := ParsePhrase(text)
		if want == "" {
			if ok {
				t.Errorf("%q принято за %q", text, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%q → %q %v, ждали %q", text, got, ok, want)
		}
	}
}

// Грамматика узкая: хвосты и похожие просьбы не перехватываются, их слышит Алиса.
func TestPhraseGrammarRejectsLookalikes(t *testing.T) {
	for _, text := range []string{
		"", "Алиса", "Что дальше", "Дальше что", "Включи следующую песню группы кино", "Пауза на пять минут", "Поставь таймер на паузу",
		"Продолжи сказку", "Продолжи рассказ про волка", "Включи музыку Кино", "Включи музыку для сна", "Включи музыку группы Ария", "Включи Кино", "Стоп машина", "Останови будильник",
		"Стоп", "Алиса, стоп", "Останови", "Остановись", // так же останавливают звонок будильника и таймера: это фраза Алисы
		"Выключи свет", "Назад в будущее", "Включи назад в будущее", "Сколько времени", "Громче", "Тише", "Что играет", "Дальше по плану",
		"Лайк", "Включи музыку", // это дело самого плеера, а не управление воспроизведением
	} {
		if c, ok := ParsePhrase(text); ok {
			t.Errorf("%q принято за %q, а это не наша фраза", text, c)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Алиса, ПАУЗА!!":       "пауза",
		"Ёлка":                 "елка",
		"поставь  на   паузу":  "поставь на паузу",
		"Алиса, пожалуйста...": "",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, ждали %q", in, got, want)
		}
	}
}

func TestParseVirtual(t *testing.T) {
	for name, want := range map[string]Cmd{"pause": Pause, "release": Pause, "play": Resume} {
		if c, ok := ParseVirtual(name); !ok || c != want {
			t.Errorf("%s → %q %v, ждали %q", name, c, ok, want)
		}
	}
	if c, ok := ParseVirtual("other"); ok {
		t.Errorf("чужая команда принята за %q", c)
	}
}

func TestParseDirective(t *testing.T) {
	for name, want := range map[string]Cmd{"player_pause": Pause, "player_continue": Resume, "player_next_track": Next, "player_previous_track": Prev} {
		if c, ok := ParseDirective(name); !ok || c != want {
			t.Errorf("%s → %q %v, ждали %q", name, c, ok, want)
		}
	}
	for _, name := range []string{"draw_animation_directive", "tts_play_placeholder", "player_like", ""} {
		if c, ok := ParseDirective(name); ok {
			t.Errorf("директива %q принята за %q", name, c)
		}
	}
}
