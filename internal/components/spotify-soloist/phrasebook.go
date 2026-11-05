package spotifysoloist

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Книга фраз. Как зовут сам Spotify в голосовых просьбах («спотифай», «спотик», «Spotify»), какие слова стоят перед названием («в», «на»,
// «со») и какие целые фразы просто запускают Spotify — всё это лежит в файле <каталог>/phrases.txt, а не в настройках: список длинный, его правят
// руками (nano) и склоняют слова («в спотифае», «со спотифая»). Файл создаётся при первом запуске компонента со встроенными значениями, читается
// заново, когда меняется (перезапуск и config update не нужны), а при ошибке в нём остаются прежние фразы и в журнале пишется, что не так.
//
// Распознаватель Алисы пишет «Spotify» по-английски, а не «спотифай»; слова сравниваются после нормализации (строчные буквы, без знаков и «ё»),
// так что и «Spotify», и «спотифай» должны быть в списке названий.

const phraseFileName = "phrases.txt"

// defaultPhrases — содержимое файла по умолчанию (и встроенная книга, когда файла нет).
const defaultPhrases = `# Фразы Spotify для голосовых просьб. Файл правится руками (nano), читается заново сам, когда меняется: перезапуск не нужен.
# Одна строка — одно слово или фраза. Регистр, знаки препинания и «ё» не важны. Всё после # — комментарий.
# Что именно услышала Алиса, видно в журнале диалога (отладочная страница, openmediad logs): распознаватель пишет Spotify по-английски.
# Проверить, как поймётся фраза:  openmediad spotify phrases test "включи музыку в спотике"

[names]
# Как называют Spotify. Склонять надо самим: «в спотифае», «со спотифая».
spotify
спотифай
спотифае
спотифая
спотифаю
спотифаем
спотик
спотике
спотика
спотику
спотиком

[prepositions]
# Слова, которые стоят перед названием сервиса и не относятся к названию песни: «включи Кино НА спотифае».
в
на
из
со
через

[start]
# Целые фразы, которые просто запускают Spotify (продолжают прошлую сессию или играют music_uri из настроек), даже если играет Яндекс.
# Сами собираются «включи / запусти / открой / поставь + название» («включи спотифай») и «включи музыку + предлог + название»
# («включи музыку в спотике»); сюда — всё остальное. Примеры (уберите # слева):
# давай слушать музыку на спотифае
# спотифай
`

// phraseBook — разобранный файл фраз.
type phraseBook struct {
	names  map[string]bool // слова-названия сервиса
	preps  map[string]bool // предлоги перед названием, которые уходят вместе с ним
	starts map[string]bool // целые фразы, запускающие Spotify
}

// parsePhraseBook разбирает файл. Ошибка — файл не принят целиком (строка и что не так); warns — подозрительное, но допустимое.
func parsePhraseBook(text string) (b *phraseBook, warns []string, err error) {
	b = &phraseBook{names: map[string]bool{}, preps: map[string]bool{}, starts: map[string]bool{}}
	section := ""
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		if h := strings.IndexByte(line, '#'); h >= 0 {
			line = line[:h]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, nil, fmt.Errorf("строка %d: раздел %q без закрывающей скобки", n, line)
			}
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			switch section {
			case "names", "prepositions", "start":
			default:
				return nil, nil, fmt.Errorf("строка %d: нет раздела [%s] (есть [names], [prepositions], [start])", n, section)
			}
			continue
		}
		if section == "" {
			return nil, nil, fmt.Errorf("строка %d: %q стоит до первого раздела ([names], [prepositions] или [start])", n, line)
		}
		w := normalize(line)
		if w == "" {
			return nil, nil, fmt.Errorf("строка %d: %q после разбора пусто (слова вроде «алиса» и «пожалуйста» в расчёт не идут)", n, line)
		}
		switch section {
		case "names":
			if strings.Contains(w, " ") {
				return nil, nil, fmt.Errorf("строка %d: %q — в [names] по одному слову в строке", n, line)
			}
			b.names[w] = true
		case "prepositions":
			if strings.Contains(w, " ") {
				return nil, nil, fmt.Errorf("строка %d: %q — в [prepositions] по одному слову в строке", n, line)
			}
			b.preps[w] = true
		case "start":
			if len(strings.Fields(w)) > 8 {
				return nil, nil, fmt.Errorf("строка %d: фраза длиннее восьми слов", n)
			}
			b.starts[w] = true
		}
	}
	if len(b.names) == 0 && len(b.starts) == 0 {
		warns = append(warns, "ни одного названия и ни одной фразы запуска: Spotify по голосу не запускается («включи спотифай» уйдёт Алисе)")
	}
	return b, warns, nil
}

// builtinBook — встроенные фразы: пока файла нет или он не читается.
var builtinBook = func() *phraseBook {
	b, _, err := parsePhraseBook(defaultPhrases)
	if err != nil {
		panic("phrases: встроенный файл фраз не разбирается: " + err.Error())
	}
	return b
}()

// mentions — есть ли среди слов название сервиса.
func (b *phraseBook) mentions(words []string) bool {
	for _, w := range words {
		if b.names[w] {
			return true
		}
	}
	return false
}

// summary — что в книге, коротко (для статуса и команды phrases).
func (b *phraseBook) summary() string {
	return fmt.Sprintf("названий %d, предлогов %d, целых фраз запуска %d", len(b.names), len(b.preps), len(b.starts))
}

func (b *phraseBook) sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// bookStore держит книгу фраз и перечитывает файл, когда он изменился (проверка — один stat на просьбу).
type bookStore struct {
	path string
	logf func(string, ...any)

	mu    sync.Mutex
	book  *phraseBook
	stamp time.Time
	size  int64
	have  bool // файл был прочитан хотя бы раз (или не читался из-за ошибки)
}

func newBookStore(path string, logf func(string, ...any)) *bookStore {
	return &bookStore{path: path, logf: logf, book: builtinBook}
}

// get — актуальная книга.
func (s *bookStore) get() *phraseBook {
	st, err := os.Stat(s.path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil { // файла нет — встроенные фразы
		if s.have {
			s.book, s.stamp, s.size, s.have = builtinBook, time.Time{}, 0, false
		}
		return s.book
	}
	if s.have && st.ModTime().Equal(s.stamp) && st.Size() == s.size {
		return s.book
	}
	s.stamp, s.size, s.have = st.ModTime(), st.Size(), true
	data, err := os.ReadFile(s.path)
	if err != nil {
		s.logf("фразы Spotify: %s не читается: %v (остались прежние)", s.path, err)
		return s.book
	}
	b, warns, err := parsePhraseBook(string(data))
	if err != nil {
		s.logf("фразы Spotify: %s: %v. Остались прежние фразы; исправьте файл и проверьте: openmediad spotify phrases", s.path, err)
		return s.book
	}
	for _, w := range warns {
		s.logf("фразы Spotify: %s", w)
	}
	s.logf("фразы Spotify перечитаны из %s: %s", s.path, b.summary())
	s.book = b
	return b
}

// ensure создаёт файл со встроенными фразами, если его нет, а каталог установки уже есть. true — создан.
func (s *bookStore) ensure() (bool, error) {
	if _, err := os.Stat(s.path); err == nil {
		return false, nil
	}
	if _, err := os.Stat(filepath.Dir(s.path)); err != nil {
		return false, nil // Spotify ещё не установлен
	}
	return true, os.WriteFile(s.path, []byte(defaultPhrases), 0o644)
}

// write записывает встроенные фразы в файл (команда phrases reset); каталог создаёт.
func (s *bookStore) write() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, []byte(defaultPhrases), 0o644)
}
