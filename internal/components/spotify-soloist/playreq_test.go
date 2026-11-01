package spotifysoloist

import "testing"

func TestPlayRequestParsing(t *testing.T) {
	cases := []struct {
		text     string
		query    string
		kind     playKind
		explicit bool
	}{
		{"Включи Кино", "кино", kindAny, false},
		{"Алиса, включи Билли Айлиш", "билли айлиш", kindAny, false},
		{"Включи Группа крови", "группа крови", kindAny, false},
		{"Поставь рок", "рок", kindAny, false},
		{"Включи музыку группы Кино", "кино", kindArtist, true},
		{"Включи музыку Кино", "кино", kindAny, true},
		{"Включи песню Группа крови", "группа крови", kindTrack, true},
		{"Включи песню Группа крови группы Кино", "группа крови кино", kindTrack, true},
		{"Включи трек Fade To Black", "fade to black", kindTrack, true},
		{"Включи альбом Ocean Eyes", "ocean eyes", kindAlbum, true},
		{"Включи плейлист для сна", "для сна", kindPlaylist, true},
		{"Поставь мне исполнителя Нойз МС", "нойз мс", kindArtist, true},
		{"Включи музыку в стиле джаз", "джаз", kindGenre, true},
		{"Включи жанр рок", "рок", kindGenre, true},
		{"Включи музыку для сна", "для сна", kindAny, true},
		{"Запусти музыку Кино пожалуйста", "кино", kindAny, true},
		{"Хочу послушать Кино", "кино", kindAny, false},
		{"Сыграй мне Группа крови", "группа крови", kindAny, false},
		{"Давай включи альбом Ночные снайперы", "ночные снайперы", kindAlbum, true},
	}
	for _, c := range cases {
		got, ok := parsePlayRequest(c.text)
		if !ok {
			t.Errorf("%q не разобрана", c.text)
			continue
		}
		if got.Query != c.query || got.Kind != c.kind || got.Explicit != c.explicit {
			t.Errorf("%q → запрос %q, вид %v, явная %v; ждали %q, %v, %v", c.text, got.Query, got.Kind, got.Explicit, c.query, c.kind, c.explicit)
		}
	}
}

// Всё, что не про музыку или не про «включи что-то», разбор не пропускает (а «голые» просьбы всё равно ждут вердикта облака).
func TestPlayRequestRejectsNonMusic(t *testing.T) {
	for _, text := range []string{
		"", "Включи", "Включи свет", "Включи свет в комнате", "Включи лампу", "Включи телевизор", "Включи ТВ", "Включи кондиционер", "Выключи свет",
		"Включи будильник на семь", "Включи таймер на пять минут", "Включи режим не беспокоить", "Включи радио", "Включи погоду",
		"Включи розетку на кухне", "Включи фильм Дюна", "Включи новости", "Включи сказку", "Включи ютуб", "Включи сценарий вечер",
		"Какая погода", "Сколько времени", "Что играет", "Пауза", "Включи музыку", "Включи песню", "Поставь альбом",
		"Включи громкость на пять", "Включи блютус", "Поставь будильник", "Поставь таймер", "Запусти пылесос", "Включи подсветку",
		"Включи что-то очень длинное что никак не может быть названием музыки потому что слишком много слов в нём",
	} {
		if got, ok := parsePlayRequest(text); ok {
			t.Errorf("%q принята как просьба: запрос %q, вид %v", text, got.Query, got.Kind)
		}
	}
}

// «Включи музыку» без названия обрабатывает отдельная фраза (voice.go), а не поиск.
func TestBareIncludeMusicIsNotASearch(t *testing.T) {
	if _, ok := parsePlayRequest("Включи музыку"); ok {
		t.Fatal("«включи музыку» принята за поиск")
	}
	if parsePhrase("Включи музыку") != actPlayMusic {
		t.Fatal("«включи музыку» перестала быть отдельной фразой")
	}
}
