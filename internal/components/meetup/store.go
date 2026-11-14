package meetup

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Settings — то, что человек меняет на странице настроек, командой `openmediad meetup` и из чата. Хранится не в файле
// настроек демона, а в отдельном файле состояния с правами 0600: здесь секреты (ссылка на конференцию с кодом комнаты и,
// возможно, ключом шифрования, пароль комнаты, фраза шифрования, сессия аккаунта), а файл настроек показывают команды и журнал.
// Пароль аккаунта не хранится нигде: он один раз меняется на сессию.
type Settings struct {
	Link    string `json:"link"`    // ссылка на конференцию целиком
	Name    string `json:"name"`    // имя участника (у входа без аккаунта)
	Connect bool   `json:"connect"` // держать подключение к комнате
	Audio   bool   `json:"audio"`   // отдавать звук движка в конференцию
	Chat    bool   `json:"chat"`    // выполнять команды из чата
	Alice   bool   `json:"alice"`   // разрешить в чате свободные фразы Алисе (!алиса …, !play <запрос>)
	Volume  int    `json:"volume"`  // громкость звука в конференции, 0–100

	RoomPassword string       `json:"room_password,omitempty"` // пароль личной комнаты или ссылки-приглашения
	Phrase       string       `json:"e2e_phrase,omitempty"`    // фраза сквозного шифрования (если ключа нет в ссылке)
	Account      SavedAccount `json:"account"`                 // вход под аккаунтом MeetUp
	ObeyNew      bool         `json:"obey_new"`                // слушаться участников, о которых нет личного правила
	Rules        []Rule       `json:"rules,omitempty"`         // личные правила: кого слушаться, кого нет
}

// SavedAccount — сохранённый вход в аккаунт: сессия действует только на сервере, который её выдал.
type SavedAccount struct {
	Host   string `json:"host,omitempty"`   // сервер (как в ссылке)
	Secure bool   `json:"secure,omitempty"` // сервер по https
	Login  string `json:"login,omitempty"`
	Name   string `json:"name,omitempty"`    // отображаемое имя из профиля
	UserID int64  `json:"user_id,omitempty"` // id аккаунта на сервере
	Token  string `json:"token,omitempty"`   // кука сессии: секрет
}

// Rule — личное правило для участника: слушаться его команд или нет.
type Rule struct {
	Key   string `json:"key"`   // кто (ruleKey)
	Name  string `json:"name"`  // имя на момент создания правила: для показа
	Allow bool   `json:"allow"` // слушаться
}

// maxRules — сколько личных правил хранится: старые вытесняются.
const maxRules = 200

// ruleKey — по чему участника узнают в следующий раз: сервер и аккаунт, а у вошедшего без аккаунта — сервер и имя. Имя без
// аккаунта никем не подтверждено (его может взять кто угодно), поэтому правилам для таких участников доверия меньше.
func ruleKey(host string, userID int64, name string) string {
	if userID > 0 {
		return host + "|u|" + strconv.FormatInt(userID, 10)
	}
	return host + "|n|" + strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// obeys — слушаться ли участника с ключом key: личное правило, а без него — правило для новых.
func (s *Settings) obeys(key string) (allow, ruled bool) {
	for _, r := range s.Rules {
		if r.Key == key {
			return r.Allow, true
		}
	}
	return s.ObeyNew, false
}

// setRule ставит (allow != nil) или забывает (nil) личное правило.
func (s *Settings) setRule(key, name string, allow *bool) {
	s.Rules = slices.DeleteFunc(slices.Clone(s.Rules), func(r Rule) bool { return r.Key == key })
	if allow != nil {
		s.Rules = append(s.Rules, Rule{Key: key, Name: name, Allow: *allow})
	}
	if len(s.Rules) > maxRules {
		s.Rules = s.Rules[len(s.Rules)-maxRules:]
	}
}

func defaultSettings(name string) Settings {
	return Settings{Name: name, Audio: true, Chat: true, Alice: true, Volume: 50, ObeyNew: true}
}

// normalize приводит настройки к допустимым значениям.
func (s *Settings) normalize(defaultName string) {
	s.Link = strings.TrimSpace(s.Link)
	s.Name = strings.Join(strings.Fields(s.Name), " ")
	if s.Name == "" {
		s.Name = defaultName
	}
	if utf8.RuneCountInString(s.Name) > 64 {
		s.Name = string([]rune(s.Name)[:64])
	}
	s.Volume = min(max(s.Volume, 0), 100)
	if len(s.RoomPassword) > 256 {
		s.RoomPassword = s.RoomPassword[:256]
	}
	if len(s.Phrase) > 1024 {
		s.Phrase = s.Phrase[:1024]
	}
	if s.Account.Token == "" {
		s.Account = SavedAccount{}
	}
}

// loadSettings читает файл состояния; нет файла — умолчания.
func loadSettings(path, defaultName string) (Settings, error) {
	s := defaultSettings(defaultName)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return defaultSettings(defaultName), err
	}
	s.normalize(defaultName)
	return s, nil
}

// saveSettings пишет файл состояния атомарно (новый файл + переименование), с правами только для владельца.
func saveSettings(path string, s Settings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := path + ".new"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// vendorCA — набор корневых сертификатов прошивки: у Go на колонке нет системного хранилища в привычном месте
// (то же делает yandex-music/fetch.go; общим кодом это станет, когда появится третий пользователь).
const vendorCA = "/system/vendor/quasar/ca-certificates.crt"

// tlsConfig — проверка сертификата сервера MeetUp по системным корням, при их отсутствии — по набору прошивки.
// Заглушка: сервер с самоподписанным сертификатом (MeetUp без домена) так не пройдёт; нужна сверка по отпечатку (README.md).
func tlsConfig() *tls.Config {
	pool, _ := x509.SystemCertPool()
	if pool == nil || len(pool.Subjects()) == 0 { //nolint:staticcheck // Subjects устарел, но нужен ровно для проверки пустоты
		pool = x509.NewCertPool()
		if pem, err := os.ReadFile(vendorCA); err == nil {
			pool.AppendCertsFromPEM(pem)
		}
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}
