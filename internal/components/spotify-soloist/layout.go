package spotifysoloist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"openmediad/internal/core/platform/layout"
)

// Раскладка установки (всё на /data, переживает перезагрузку; ОЗУ только сокет и pid в рабочем каталоге демона):
//
//	<dir>/soloist            программа Spotify (скачивается при установке, не входит в проект)
//	<dir>/lib/               glibc 2.41 из Debian, рядом лежит шим libpulse.so.0 (вшит в бинарник openmediad)
//	<dir>/data/              сеанс и настройки Soloist (-D)
//	<dir>/cache/             звуковой кэш (-C)
//	<dir>/api.key            ключ Soloist, права 0600 (или key_file из настроек)
//	<dir>/install.json       что и когда установлено
const defaultSubdir = "spotify-soloist"

type paths struct {
	dir     string // каталог установки
	run     string // рабочий каталог демона (сокет, pid)
	keyFile string // свой файл ключа; пусто — <dir>/api.key
}

func newPaths(dir, run, keyFile string) paths {
	if dir == "" {
		dir = filepath.Join(layout.Home(), defaultSubdir)
	}
	return paths{dir: dir, run: run, keyFile: keyFile}
}

func (p paths) soloist() string { return filepath.Join(p.dir, "soloist") }
func (p paths) lib() string     { return filepath.Join(p.dir, "lib") }
func (p paths) loader() string  { return filepath.Join(p.lib(), "ld-linux-armhf.so.3") }
func (p paths) shim() string    { return filepath.Join(p.lib(), "libpulse.so.0") }
func (p paths) data() string    { return filepath.Join(p.dir, "data") }
func (p paths) cache() string   { return filepath.Join(p.dir, "cache") }
func (p paths) meta() string    { return filepath.Join(p.dir, "install.json") }
func (p paths) sock() string    { return filepath.Join(p.run, "spotify-soloist.sock") }
func (p paths) pid() string     { return filepath.Join(p.run, "spotify-soloist.pid") }

func (p paths) key() string {
	if p.keyFile != "" {
		return p.keyFile
	}
	return filepath.Join(p.dir, "api.key")
}

// missing — чего не хватает для запуска (пусто — всё на месте).
func (p paths) missing() []string {
	var out []string
	for _, f := range []struct{ path, what string }{
		{p.soloist(), "программа Soloist"},
		{p.loader(), "glibc (загрузчик)"},
		{filepath.Join(p.lib(), "libc.so.6"), "glibc (libc)"},
		{p.shim(), "шим libpulse"},
		{p.key(), "ключ Soloist"},
	} {
		if st, err := os.Stat(f.path); err != nil || st.IsDir() {
			out = append(out, f.what)
		}
	}
	return out
}

// installMeta — запись об установке (install.json).
type installMeta struct {
	Soloist struct {
		Version     string    `json:"version"`      // «1.3.10.31»
		BuildDate   string    `json:"build_date"`   // «20261030»
		SHA256      string    `json:"sha256"`       // хэш скачанного архива
		InstalledAt time.Time `json:"installed_at"` //
	} `json:"soloist"`
	Glibc struct {
		Package string `json:"package"` // имя пакета Debian
		SHA256  string `json:"sha256"`
	} `json:"glibc"`
	ShimSHA256 string `json:"shim_sha256"`
}

func readMeta(path string) (installMeta, error) {
	var m installMeta
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

func writeMeta(path string, m installMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o644)
}

// writeFileAtomic пишет файл через временное имя и переименование: обрыв не оставит половину.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// buildLife — срок жизни сборки Soloist (каждая истекает через 90 дней, код выхода 10).
const buildLife = 90 * 24 * time.Hour

var buildDateRE = regexp.MustCompile(`^\d{8}$`)

// expires — когда истекает установленная сборка (по дате сборки); ok=false — дата неизвестна.
func (m installMeta) expires() (time.Time, bool) {
	if !buildDateRE.MatchString(m.Soloist.BuildDate) {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102", m.Soloist.BuildDate)
	if err != nil {
		return time.Time{}, false
	}
	return t.Add(buildLife), true
}

// expiryText — «осталось N дн.» / «истекла».
func expiryText(m installMeta, now time.Time) string {
	e, ok := m.expires()
	if !ok {
		return "срок сборки неизвестен"
	}
	days := int(e.Sub(now).Hours() / 24)
	if days < 0 {
		return "сборка Soloist истекла: openmediad spotify update"
	}
	return fmt.Sprintf("сборка Soloist действует ещё %d дн.", days)
}
