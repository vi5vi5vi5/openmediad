package spotifysoloist

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDefaultPhraseFileParses(t *testing.T) {
	b, warns, err := parsePhraseBook(defaultPhrases)
	if err != nil || len(warns) != 0 {
		t.Fatalf("встроенный файл: %v %v", err, warns)
	}
	// распознаватель Алисы пишет Spotify по-английски: оба написания должны быть в списке
	for _, w := range []string{"spotify", "спотифай", "спотифае", "спотик", "спотике"} {
		if !b.names[w] {
			t.Errorf("нет названия %q", w)
		}
	}
	for _, w := range []string{"в", "на", "из", "со"} {
		if !b.preps[w] {
			t.Errorf("нет предлога %q", w)
		}
	}
	if len(b.starts) != 0 {
		t.Fatalf("в файле по умолчанию целые фразы закомментированы: %v", b.starts)
	}
}

func TestPhraseFileFormat(t *testing.T) {
	b, _, err := parsePhraseBook("# заголовок\n\n[Names]\nSpotify   # с большой буквы\nСпотичек\n[prepositions]\nЁ\n[start]\nДавай  слушать, Музыку!\n")
	if err != nil {
		t.Fatal(err)
	}
	if !b.names["spotify"] || !b.names["спотичек"] || !b.preps["е"] || !b.starts["слушать музыку"] {
		t.Fatalf("разобрано: %+v", b)
	}
}

func TestPhraseFileErrorsNameTheLine(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"до раздела":        {"спотик\n", "строка 1"},
		"нет раздела":       {"[names]\nспотик\n[verbs]\nвключи\n", "строка 3: нет раздела [verbs]"},
		"скобка":            {"[names\n", "без закрывающей"},
		"два слова в names": {"[names]\nспотик спотифай\n", "строка 2"},
		"пусто после слов":  {"[prepositions]\nалиса\n", "после разбора пусто"},
		"слишком длинная":   {"[start]\nодин два три четыре пять шесть семь восемь девять\n", "длиннее восьми слов"},
	} {
		_, _, err := parsePhraseBook(tc.text)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: ошибка %v, ждали упоминание %q", name, err, tc.want)
		}
	}
	if _, warns, err := parsePhraseBook("[names]\n"); err != nil || len(warns) != 1 {
		t.Fatalf("пустой файл должен быть принят с предупреждением: %v %v", err, warns)
	}
}

func newTestStore(t *testing.T) (*bookStore, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var logs []string
	s := newBookStore(filepath.Join(t.TempDir(), "phrases.txt"), func(f string, a ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(f, a...))
		mu.Unlock()
	})
	return s, &logs
}

func TestStoreUsesBuiltinWhenThereIsNoFile(t *testing.T) {
	s, _ := newTestStore(t)
	if s.get() != builtinBook {
		t.Fatal("без файла должна работать встроенная книга")
	}
}

func TestStoreCreatesTheFileOnlyWhereSpotifyIsInstalled(t *testing.T) {
	s, _ := newTestStore(t)
	if created, err := s.ensure(); err != nil || !created {
		t.Fatalf("каталог есть, файла нет: %v %v", created, err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil || string(data) != defaultPhrases {
		t.Fatalf("файл: %v", err)
	}
	os.WriteFile(s.path, []byte("[names]\nсвоё\n"), 0o644)
	if created, _ := s.ensure(); created {
		t.Fatal("чужие правки затёрты")
	}
	missing := newBookStore(filepath.Join(t.TempDir(), "нет", "phrases.txt"), nil)
	if created, err := missing.ensure(); created || err != nil {
		t.Fatalf("Spotify не установлен (каталога нет): файл не создаётся молча: %v %v", created, err)
	}
}

// Файл правят на ходу: перечитывается сам; сломанный не принимается (остаётся прежний), починенный подхватывается; удалённый — снова встроенные.
func TestStoreFollowsTheFile(t *testing.T) {
	s, logs := newTestStore(t)
	write := func(text string) {
		os.WriteFile(s.path, []byte(text), 0o644)
		// время изменения может не успеть смениться: размер файла у каждой версии свой, а stat сравнивает и его
	}
	write("[names]\nспотичек\n[prepositions]\nв\n")
	b := s.get()
	if !b.names["спотичек"] || b.names["спотик"] {
		t.Fatalf("файл не прочитан: %+v", b.names)
	}
	write("[names]\nспотичек\nещё одно\n")
	if got := s.get(); !got.names["спотичек"] || len(got.names) != 1 {
		t.Fatal("сломанный файл принят")
	}
	if n := countContaining(*logs, "строка 3"); n != 1 {
		t.Fatalf("в журнале должна быть одна жалоба на строку: %v", *logs)
	}
	s.get()
	s.get()
	if n := countContaining(*logs, "строка 3"); n != 1 {
		t.Fatalf("жалоба повторяется на каждую просьбу: %v", *logs)
	}
	write("[names]\nспотичек\nдругое\n")
	if got := s.get(); !got.names["другое"] {
		t.Fatal("починенный файл не подхвачен")
	}
	os.Remove(s.path)
	if s.get() != builtinBook {
		t.Fatal("файл удалён — должны вернуться встроенные фразы")
	}
}

func countContaining(list []string, sub string) (n int) {
	for _, s := range list {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// Правка того же размера и в ту же секунду тоже замечается, если сменилось время: проверяем явно, подменив его.
func TestStoreNoticesAnEditWithTheSameSize(t *testing.T) {
	s, _ := newTestStore(t)
	os.WriteFile(s.path, []byte("[names]\nспотик\n"), 0o644)
	first := s.get()
	os.WriteFile(s.path, []byte("[names]\nспотяк\n"), 0o644)
	later := time.Now().Add(time.Hour)
	os.Chtimes(s.path, later, later)
	if second := s.get(); second == first || !second.names["спотяк"] {
		t.Fatal("правка того же размера не замечена")
	}
}
