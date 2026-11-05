package spotifysoloist

import (
	playctl "openmediad/internal/core/control"
)

// Грамматика фраз самого Spotify: лайк и «включи музыку». Управление воспроизведением («пауза», «дальше», «назад», «продолжи») здесь не живёт:
// это дело ядра (core/control, phrases.go), оно одинаково для любого плеера. Грамматика намеренно узкая: подходит только фраза целиком, без
// хвостов. Чем меньше фраз, тем меньше шансов перехватить то, что относилось к чему-то другому; новые фразы добавляются в таблицу и в тест рядом.

// action — что компонент делает по фразе помимо управления воспроизведением.
type action int

const (
	actNone action = iota
	actLike
	actUnlike
	actPlayMusic
	actStartService // «включи спотифай»: Spotify назван прямо
)

func (a action) String() string {
	switch a {
	case actLike:
		return "лайк"
	case actUnlike:
		return "снять лайк"
	case actPlayMusic:
		return "включить музыку"
	case actStartService:
		return "запустить Spotify по названию"
	}
	return "—"
}

// phraseTable — нормализованная фраза (строчные, без «ё», без знаков и слов-связок) → действие.
var phraseTable = buildPhrases(map[action][]string{
	actPlayMusic: {
		"включи музыку", "поставь музыку", "включи музон", "включи мне музыку", "поставь мне музыку", "включи какую нибудь музыку",
		"поставь какую нибудь музыку", "включи любую музыку", "включи что нибудь", "поставь что нибудь", "музыку", "музыку включи", "музыка",
	},
	actLike: {
		"лайк", "поставь лайк", "поставить лайк", "лайкни", "лайкни песню", "лайкни трек", "лайк песне", "лайк треку", "поставь лайк песне",
		"поставь лайк треку", "поставь лайк этой песне", "поставь лайк этому треку", "мне нравится", "мне нравится эта песня", "мне нравится этот трек",
		"нравится", "нравится песня", "добавь в избранное", "добавь песню в избранное", "добавь трек в избранное",
	},
	actUnlike: {
		"убери лайк", "сними лайк", "удали лайк", "убрать лайк", "отмени лайк", "убери лайк с песни", "убери лайк с трека", "мне больше не нравится",
		"убери из избранного", "удали из избранного", "убери песню из избранного", "удали песню из избранного",
	},
})

func buildPhrases(src map[action][]string) map[string]action {
	m := map[string]action{}
	for a, list := range src {
		for _, p := range list {
			if n := normalize(p); n != p || n == "" {
				panic("phrases: фраза не в нормальной форме: " + p)
			}
			if prev, dup := m[p]; dup && prev != a {
				panic("phrases: фраза в двух действиях: " + p)
			}
			if _, transport := playctl.ParsePhrase(p); transport {
				panic("phrases: фраза уже значит управление плеером в ядре: " + p)
			}
			m[p] = a
		}
	}
	return m
}

// normalize приводит фразу к виду таблицы: строчные буквы, «е» вместо «ё», знаки пропали, слова-связки убраны (одинаково для всех плееров).
func normalize(s string) string { return playctl.Normalize(s) }

// parsePhrase — действие, которое значит фраза (actNone — фраза не из грамматики).
func parsePhrase(text string) action {
	return phraseTable[normalize(text)]
}
