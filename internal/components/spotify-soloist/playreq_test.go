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

// «Включи спотифай» и родня: Spotify назван, а искать нечего — просто запустить его. Распознаватель пишет «Spotify» по-английски: так тоже работает.
func TestStartRequestsNameTheServiceOnly(t *testing.T) {
	for _, text := range []string{
		"Включи спотифай", "Включи Spotify", "Включи spotify.", "Включи спотик", "Запусти спотифай", "Открой спотифай", "Открой Spotify", "Включи мне спотик",
		"Алиса, включи спотифай пожалуйста", "Поставь спотик", "Переключись на спотифай", "Хочу послушать спотифай",
		"Включи музыку в спотифае", "Включи музыку на Spotify", "Включи музыку в спотике", "Включи музыку со спотифая", "Включи музыку из спотика",
		"Включи что-нибудь из спотика", "Поставь музыку на спотифае", "Включи спотифай музыку", "Включи песню в спотифае", "Запусти музыку в Spotify",
	} {
		got, ok := parsePlayRequest(text)
		if !ok || !got.Start || !got.Explicit {
			t.Errorf("%q → %+v %v, ждали запуск Spotify", text, got, ok)
		}
	}
}

// Всё, что называет Spotify и просит конкретное, остаётся поиском; а без названия сервиса «открой», «переключись» и прочее — не наши глаголы.
func TestStartRequestsDoNotSwallowSearches(t *testing.T) {
	for text, query := range map[string]string{
		"Включи Кино на спотифае":             "кино",
		"Включи в Spotify группу Кино":        "кино",
		"Включи песню Группа крови в спотике": "группа крови",
	} {
		got, ok := parsePlayRequest(text)
		if !ok || got.Start || got.Query != query || !got.Explicit {
			t.Errorf("%q → %+v %v, ждали поиск %q", text, got, ok, query)
		}
	}
	for _, text := range []string{
		"Открой окно", "Открой дверь", "Переключись на второй канал", "Переключи канал", "Перейди на главную", "Включи яндекс музыку в спотифае", "Спотифай",
		"Что такое спотифай", "Включи свет",
	} {
		if got, ok := parsePlayRequest(text); ok && (got.Start || text != "Включи свет" && got.Explicit) {
			t.Errorf("%q принято за запуск Spotify: %+v", text, got)
		}
	}
}

// Свои слова из файла: новое название работает, убранное — нет, а целые фразы запускают Spotify как есть.
func TestStartRequestsFollowTheBook(t *testing.T) {
	b, _, err := parsePhraseBook("[names]\nспотичек\nспотичке\n[prepositions]\nна\n[start]\nдавай слушать музыку на спотичке\nспотичек\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Включи спотичек", "Включи музыку на спотичке", "Давай слушать музыку на спотичке", "Спотичек"} {
		if got, ok := b.parsePlayRequest(text); !ok || !got.Start {
			t.Errorf("%q не запускает Spotify по этой книге: %+v %v", text, got, ok)
		}
	}
	if got, ok := b.parsePlayRequest("Включи спотик"); ok && got.Start {
		t.Error("«спотик» убран из книги, а всё ещё запускает Spotify")
	}
}

// Распознаватель ставит глагол в конец («Spotify включи» — запись из журнала): когда Spotify назван, это та же просьба.
func TestVerbAtTheEndWhenSpotifyIsNamed(t *testing.T) {
	for _, text := range []string{"Spotify включи", "Спотифай запусти", "Спотик включи пожалуйста", "Музыку в спотифае включи", "Включи spotify"} {
		if got, ok := parsePlayRequest(text); !ok || !got.Start {
			t.Errorf("%q → %+v %v, ждали запуск Spotify", text, got, ok)
		}
	}
	if got, ok := parsePlayRequest("Кино на спотифае включи"); !ok || got.Start || got.Query != "кино" || !got.Explicit {
		t.Errorf("«Кино на спотифае включи» → %+v %v, ждали поиск «кино»", got, ok)
	}
	for _, text := range []string{"Свет включи", "Кино включи", "Включи", "Окно открой", "Спотифай"} {
		if got, ok := parsePlayRequest(text); ok && (got.Start || got.Explicit) {
			t.Errorf("%q принято за просьбу про Spotify: %+v", text, got)
		}
	}
}
