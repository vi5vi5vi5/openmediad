package soundcloud

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"openmediad/internal/core/config"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/led"
	"openmediad/internal/core/platform/layout"
	"openmediad/internal/core/registry"
	"openmediad/internal/libs/aac"
)

// Config — секция [soundcloud].
type Config struct {
	config.Toggle
	ClientID  string `conf:"client_id"`  // client_id веб-плеера SoundCloud; пусто — найти на сайте самому
	MaxTracks int    `conf:"max_tracks"` // сколько треков плейлиста или исполнителя ставить в очередь
	Proxy     string `conf:"proxy"`      // прокси для всех запросов к SoundCloud (http://хост:порт, socks5://хост:порт); пусто — напрямую
}

const template = `[soundcloud]             # SoundCloud (опытный): трек, плейлист, альбом или исполнитель по ссылке — openmediad player link ССЫЛКА, !play ССЫЛКА в MeetUp
enabled = true
client_id = ""           # client_id веб-плеера SoundCloud; пусто — колонка находит его на soundcloud.com сама и ищет заново, когда он устареет
max_tracks = 200         # сколько треков плейлиста или исполнителя ставить в очередь (1–500)
proxy = ""               # прокси для SoundCloud: http://хост:порт или socks5://хост:порт; из России без него соединения сбрасываются`

func defaults() *Config {
	return &Config{Toggle: config.Toggle{Enabled: true}, MaxTracks: 200}
}

var reID = regexp.MustCompile(`^[0-9A-Za-z]{20,40}$`)

func (c *Config) validate() error {
	if c.ClientID != "" && !reID.MatchString(c.ClientID) {
		return fmt.Errorf("soundcloud.client_id: %q — нужны 20–40 латинских букв и цифр (или пусто: найти самому)", c.ClientID)
	}
	if c.MaxTracks < 1 || c.MaxTracks > 500 {
		return fmt.Errorf("soundcloud.max_tracks: %d — от 1 до 500", c.MaxTracks)
	}
	if c.Proxy != "" {
		u, err := url.Parse(c.Proxy)
		if err != nil || u.Hostname() == "" {
			return errors.New("soundcloud.proxy: нужен адрес вида http://хост:порт или socks5://хост:порт")
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("soundcloud.proxy: схема %q не поддерживается (http, https, socks5)", u.Scheme)
		}
	}
	return nil
}

// vendorCA — набор корневых сертификатов прошивки: системного хранилища в привычном для Go месте на колонке нет.
const vendorCA = "/system/vendor/quasar/ca-certificates.crt"

// newClient — HTTPS-клиент: системные сертификаты, а если их нет — набор прошивки; proxy — прокси из настроек (проверен validate).
func newClient(proxy string) *http.Client {
	var roots *x509.CertPool                                                         // nil — системные (на Windows и macOS их проверяет сама система, и пул Go выглядит пустым)
	if pool, err := x509.SystemCertPool(); err != nil || len(pool.Subjects()) == 0 { //nolint:staticcheck // Subjects устарел, но нужен ровно для проверки пустоты
		if pem, err := os.ReadFile(vendorCA); err == nil {
			roots = x509.NewCertPool()
			roots.AppendCertsFromPEM(pem)
		}
	}
	var via func(*http.Request) (*url.URL, error)
	if u, err := url.Parse(proxy); proxy != "" && err == nil {
		via = http.ProxyURL(u)
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		Proxy:                 via,
		TLSClientConfig:       &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext:           (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
	}}
}

// newComponent собирает компонент (тесты подменяют адреса API и клиент).
func newComponent(cfg Config, eng *engine.Engine, hc *http.Client, statePath string, logf func(string, ...any)) *Component {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Component{
		cfg: cfg, eng: eng, logf: logf, client: hc, newDec: aac.NewGst,
		api: &api{hc: hc, base: apiBase, site: siteBase, fixedID: cfg.ClientID, statePath: statePath, logf: logf},
	}
}

func init() {
	registry.Register(registry.Component{
		Name:     Name,
		Defaults: func() any { return defaults() },
		Template: template,
		New: func(env *registry.Env, cfg any) (any, error) {
			c := cfg.(*Config)
			if err := c.validate(); err != nil {
				return nil, err
			}
			eng, ok := registry.Use[*engine.Engine](env, "engine")
			if !ok {
				return nil, errors.New("soundcloud: нет движка")
			}
			comp := newComponent(*c, eng, newClient(c.Proxy), filepath.Join(layout.State(), "soundcloud.json"), env.Logf(Name))
			if ind, ok := registry.Use[led.Indicator](env, "led"); ok {
				comp.led = ind
			}
			return comp, nil
		},
	})
}

// NewStandalone — компонент без демона, со своим движком (openmediad-dev play-soundcloud): настройки по умолчанию и прокси, client_id
// находится на сайте и нигде не запоминается.
func NewStandalone(eng *engine.Engine, proxy string, logf func(string, ...any)) (*Component, error) {
	cfg := *defaults()
	cfg.Proxy = proxy
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return newComponent(cfg, eng, newClient(proxy), "", logf), nil
}
