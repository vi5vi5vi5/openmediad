package spotifysoloist

import "strings"

// Просьба «включи <что-то>». Её надо отличить от «включи свет», «включи телевизор», «включи будильник» и прочего, что к музыке не относится.
// Для этого два уровня:
//
//   - разбор (parsePlayRequest) смотрит только на форму фразы: глагол («включи», «поставь», «запусти», «сыграй») и, если есть, слово-категория
//     («песню», «альбом», «группу», «плейлист», «музыку»). Есть категория — просьба явно про музыку (explicit). Нет категории («включи Кино»,
//     «включи свет») — фраза может быть про что угодно;
//   - вердикт даёт сама Алиса: облако отвечает на музыкальную просьбу директивой audio_play, на «включи свет» — командой умного дома.
//     Поэтому «голые» просьбы выполняются только после того, как облако подтвердило, что это музыка (request.go).
//
// Список слов, которые заведомо не музыка (stopObjects), нужен лишь затем, чтобы не искать в Spotify «свет» зря.

// playKind — что просят найти.
type playKind int

const (
	kindAny      playKind = iota // неизвестно: исполнитель, песня, альбом или жанр
	kindTrack                    // «песню», «трек»
	kindAlbum                    // «альбом»
	kindArtist                   // «группу», «исполнителя»
	kindPlaylist                 // «плейлист», «подборку»
	kindGenre                    // «жанр», «в стиле»
)

func (k playKind) String() string {
	return [...]string{"что угодно", "трек", "альбом", "исполнитель", "плейлист", "жанр"}[k]
}

// playRequest — разобранная просьба.
type playRequest struct {
	Query    string   // что искать (нормализованный текст без глагола и слов-категорий)
	Whole    string   // всё после глагола, как сказано (для поиска среди своих названий)
	Kind     playKind // что просят
	Explicit bool     // в просьбе было слово-категория или названо само Spotify: она точно про музыку
	Start    bool     // Spotify назван, а искать нечего («включи спотифай», «включи музыку в спотике»): просто запустить Spotify
}

var playVerbs = map[string]bool{
	"включи": true, "включить": true, "поставь": true, "поставить": true, "запусти": true, "запустить": true,
	"сыграй": true, "сыграть": true, "играй": true, "воспроизведи": true, "врубай": true, "врубить": true,
}

// categories — слово-категория → что оно значит (kindAny: просто «музыка»).
var categories = map[string]playKind{
	"музыку": kindAny, "музыка": kindAny, "музон": kindAny, "музыки": kindAny,
	"песню": kindTrack, "песни": kindTrack, "песня": kindTrack, "трек": kindTrack, "треки": kindTrack, "композицию": kindTrack, "мелодию": kindTrack,
	"альбом": kindAlbum, "альбомы": kindAlbum,
	"плейлист": kindPlaylist, "плейлисты": kindPlaylist, "подборку": kindPlaylist,
	"исполнителя": kindArtist, "исполнителей": kindArtist, "артиста": kindArtist, "группу": kindArtist, "певца": kindArtist, "певицу": kindArtist,
	"жанр": kindGenre,
}

// links — слова, которые связывают категорию с названием и сами названием не являются; часть из них уточняет, что именно просят.
// Только родительный падеж: «Группа крови» — название песни, а «группы Кино» — уточнение.
var links = map[string]playKind{
	"группы": kindArtist, "исполнителя": kindArtist, "артиста": kindArtist, "певца": kindArtist, "певицы": kindArtist, "от": kindAny, "названием": kindAny, "под": kindAny, "стиле": kindGenre, "стиль": kindGenre,
	"жанре": kindGenre, "жанра": kindGenre, "жанр": kindGenre, "в": kindAny, "по": kindAny,
}

// Названия сервиса («спотифай», «спотик», «Spotify»), предлоги перед ними («в», «на») и целые фразы запуска лежат в файле phrases.txt
// (phrasebook.go), а не здесь: список длинный, его правят руками.

// startVerbs — глаголы, с которыми просьба «запусти сервис» понятна, только если назван сам Spotify («открой спотифай»; «открой окно» — не наша).
var startVerbs = map[string]bool{"открой": true, "открыть": true, "переключись": true, "переключи": true, "перейди": true, "запускай": true}

// genericRest — что может остаться от просьбы, когда слова про сервис убраны, чтобы просьба всё равно значила «просто запусти Spotify»:
// «включи музыку на спотифае», «включи что-нибудь из спотика».
var genericRest = map[string]bool{
	"": true, "музыку": true, "музыка": true, "музон": true, "музыки": true, "песню": true, "песни": true, "треки": true, "что нибудь": true,
	"что угодно": true, "любую музыку": true, "какую нибудь музыку": true, "какую нибудь песню": true, "любую песню": true, "что нибудь послушать": true,
	"музыку послушать": true, "послушать": true, "послушать музыку": true,
}

// stopObjects — первое слово такого названия означает не музыку (устройства, режимы, «видео»); без категории искать их в Spotify не нужно.
var stopObjects = map[string]bool{
	"свет": true, "света": true, "лампу": true, "лампочку": true, "люстру": true, "торшер": true, "подсветку": true, "ночник": true, "освещение": true,
	"телевизор": true, "телек": true, "тв": true, "канал": true, "канале": true, "фильм": true, "сериал": true, "мультик": true, "мультики": true,
	"кондиционер": true, "вентилятор": true, "обогреватель": true, "увлажнитель": true, "очиститель": true, "пылесос": true, "чайник": true,
	"розетку": true, "розетки": true, "утюг": true, "камеру": true, "робот": true, "кофеварку": true, "плиту": true, "духовку": true, "стиралку": true,
	"будильник": true, "таймер": true, "секундомер": true, "напоминание": true, "режим": true, "сценарий": true, "тихий": true, "ночной": true,
	"громкость": true, "звук": true, "микрофон": true, "блютус": true, "bluetooth": true, "вайфай": true, "wifi": true, "экран": true, "дисплей": true,
	"радио": true, "новости": true, "погоду": true, "подкаст": true, "аудиокнигу": true, "сказку": true, "сказки": true, "ютуб": true, "youtube": true,
	"навык": true, "игру": true, "шторы": true, "занавески": true, "кино": false, // «кино» — и фильмы, и группа: решает облако
}

// parsePlayRequest разбирает фразу по встроенной книге фраз (тесты); компонент пользуется книгой из файла.
func parsePlayRequest(text string) (playRequest, bool) { return builtinBook.parsePlayRequest(text) }

// parsePlayRequest разбирает фразу. ok=false — это не просьба что-то включить или искать нечего.
func (b *phraseBook) parsePlayRequest(text string) (req playRequest, ok bool) {
	norm := normalize(text)
	if b.starts[norm] { // целая фраза из файла: просто запустить Spotify
		return playRequest{Whole: norm, Explicit: true, Start: true}, true
	}
	t := strings.Fields(norm)
	if len(t) >= 3 && t[0] == "хочу" && t[1] == "послушать" {
		t = append([]string{"включи"}, t[2:]...)
	}
	// Распознаватель иногда ставит глагол в конец («Spotify включи»): когда Spotify назван, переносим глагол вперёд
	if n := len(t); n >= 2 && !playVerbs[t[0]] && !startVerbs[t[0]] && (playVerbs[t[n-1]] || startVerbs[t[n-1]]) && b.mentions(t[:n-1]) {
		t = append([]string{t[n-1]}, t[:n-1]...)
	}
	if len(t) < 2 || !playVerbs[t[0]] && !(startVerbs[t[0]] && b.mentions(t[1:])) {
		return req, false
	}
	rest := t[1:]
	for len(rest) > 0 && (rest[0] == "мне" || rest[0] == "нам" || rest[0] == "ка") {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return req, false
	}
	// Яндекс назван прямо — это не наша просьба; Spotify назван прямо — просьба точно про музыку, а слова про сервис в названии не нужны
	var kept []string
	for _, w := range rest {
		switch {
		case strings.HasPrefix(w, "яндекс"):
			return req, false
		case b.names[w]:
			req.Explicit = true
			if len(kept) > 0 && b.preps[kept[len(kept)-1]] {
				kept = kept[:len(kept)-1]
			}
		default:
			kept = append(kept, w)
		}
	}
	if req.Explicit && genericRest[strings.Join(kept, " ")] { // Spotify назван, а искать нечего: запустить его самого
		req.Start, req.Whole = true, strings.Join(t[1:], " ")
		return req, true
	}
	if rest = kept; len(rest) == 0 {
		return req, false
	}
	req.Whole = strings.Join(rest, " ")
	if kind, isCat := categories[rest[0]]; isCat {
		req.Explicit, req.Kind = true, kind
		rest = rest[1:]
	}
	// связующие слова в начале («группы», «в стиле», «от», «под названием») и их влияние на вид
	for len(rest) > 0 {
		kind, isLink := links[rest[0]]
		if !isLink {
			break
		}
		if kind != kindAny {
			req.Kind = kind
		}
		rest = rest[1:]
	}
	// «включи песню Группа крови группы Кино»: названия исполнителя внутри запроса оставляем, слова-уточнения выбрасываем
	var q []string
	for _, w := range rest {
		if k, isLink := links[w]; isLink && k == kindArtist {
			continue
		}
		q = append(q, w)
	}
	req.Query = strings.Join(q, " ")
	switch {
	case req.Query == "", len(q) > 8, len(req.Query) > 80:
		return req, false
	case !req.Explicit && stopObjects[q[0]]:
		return req, false
	}
	return req, true
}
