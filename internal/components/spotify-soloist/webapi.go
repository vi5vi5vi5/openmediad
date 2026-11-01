package spotifysoloist

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Лайки идут через Web API Spotify, а не через Soloist: в WebSocket API Soloist команд для библиотеки нет. Web API требует приложение
// в Developer Dashboard (Client ID — у каждого пользователя свой) и вход по OAuth. Секрета приложения не нужно: используется PKCE.
// Вход делается один раз командой `openmediad spotify login`: колонка показывает ссылку, вы открываете её на компьютере или телефоне,
// разрешаете доступ, браузер пытается открыть адрес http://127.0.0.1:8888/callback?code=… (страница не откроется, это нормально) —
// этот адрес целиком отдаётся команде. На колонке остаётся только долгоживущий refresh-токен (<каталог>/web.json, права 0600).

const (
	webRedirect = "http://127.0.0.1:8888/callback"
	webScopes   = "user-library-modify user-library-read"

	defaultAuthBase = "https://accounts.spotify.com"
	defaultAPIBase  = "https://api.spotify.com/v1"

	pendingTTL = 30 * time.Minute // сколько после выдачи ссылки ждём возвращённый адрес
)

var clientIDRe = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)
var musicURIRe = regexp.MustCompile(`^spotify:(playlist|album|artist|track):[A-Za-z0-9]{10,40}$`)

var (
	errNoClientID = errors.New("не задан client_id (Client ID приложения из Developer Dashboard Spotify): openmediad config set spotify-soloist.client_id …")
	errNoLogin    = errors.New("нет входа в Spotify Web API: выполните openmediad spotify login")
)

// webToken — что хранится на диске после входа.
type webToken struct {
	Refresh  string    `json:"refresh_token"`
	Scope    string    `json:"scope,omitempty"`
	ClientID string    `json:"client_id"` // с каким Client ID получен: с другим токен не годится
	SavedAt  time.Time `json:"saved_at"`
}

// webPending — начатый, но не законченный вход.
type webPending struct {
	Verifier string    `json:"verifier"`
	State    string    `json:"state"`
	ClientID string    `json:"client_id"`
	At       time.Time `json:"at"`
}

// webAPI — клиент Web API: вход (PKCE), обновление токена, лайки.
type webAPI struct {
	file     string        // <каталог>/web.json
	pending  string        // <каталог>/web.pending
	clientID func() string // текущий Client ID из настроек
	auth     string        // адрес accounts.spotify.com (в тестах — поддельный сервер)
	api      string        // адрес api.spotify.com/v1
	hc       *http.Client
	now      func() time.Time
	// proxy (необязательный) отдаёт адрес прокси для всех обращений к Spotify: http://, https://, socks5://. Web API закрыт для адресов
	// некоторых стран («Spotify is unavailable in this country», 403), хотя Soloist и вход в аккаунт работают.
	proxy func() string

	cmu   sync.Mutex
	pc    *http.Client // клиент с прокси
	pcURL string

	mu      sync.Mutex
	access  string
	expires time.Time
}

func newWebAPI(p paths, clientID func() string) *webAPI {
	return &webAPI{
		file: filepath.Join(p.dir, "web.json"), pending: filepath.Join(p.dir, "web.pending"), clientID: clientID,
		auth: defaultAuthBase, api: defaultAPIBase, hc: &http.Client{Timeout: 15 * time.Second}, now: time.Now,
	}
}

// client — HTTP-клиент: прямой или через прокси из настроек (меняется на ходу).
func (w *webAPI) client() *http.Client {
	if w.proxy == nil {
		return w.hc
	}
	p := w.proxy()
	if p == "" {
		return w.hc
	}
	w.cmu.Lock()
	defer w.cmu.Unlock()
	if w.pc != nil && w.pcURL == p {
		return w.pc
	}
	u, err := url.Parse(p)
	if err != nil {
		return w.hc
	}
	w.pc = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u), TLSHandshakeTimeout: 10 * time.Second}}
	w.pcURL = p
	return w.pc
}

// validProxy проверяет адрес прокси из настроек.
func validProxy(p string) error {
	if p == "" {
		return nil
	}
	u, err := url.Parse(p)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("%q: ожидается адрес вида http://хост:порт или socks5://хост:порт", p)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return nil
	}
	return fmt.Errorf("%q: схема %q не поддерживается (http, https, socks5)", p, u.Scheme)
}

// ready говорит, можно ли ставить лайки; если нет — что не так (для журнала).
func (w *webAPI) ready() error {
	id := w.clientID()
	if id == "" {
		return errNoClientID
	}
	t, err := w.loadToken()
	if err != nil || t.Refresh == "" {
		return errNoLogin
	}
	if t.ClientID != id {
		return errors.New("вход выполнен с другим Client ID: выполните openmediad spotify login заново")
	}
	return nil
}

func (w *webAPI) loadToken() (webToken, error) {
	var t webToken
	b, err := os.ReadFile(w.file)
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(b, &t)
}

func writeSecret(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// begin начинает вход: запоминает проверочные значения PKCE и возвращает ссылку для браузера.
func (w *webAPI) begin() (string, error) {
	id := w.clientID()
	if id == "" {
		return "", errNoClientID
	}
	if !clientIDRe.MatchString(id) {
		return "", errors.New("client_id выглядит неверно: это 32 символа из Developer Dashboard, без пробелов")
	}
	verifier, err := randomURLString(48)
	if err != nil {
		return "", err
	}
	state, err := randomURLString(12)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	if err := writeSecret(w.pending, webPending{Verifier: verifier, State: state, ClientID: id, At: w.now()}); err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", id)
	q.Set("redirect_uri", webRedirect)
	q.Set("scope", webScopes)
	q.Set("code_challenge_method", "S256")
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	q.Set("state", state)
	return w.auth + "/authorize?" + q.Encode(), nil
}

// finish заканчивает вход: input — адрес, на который Spotify вернул браузер (или только значение code).
func (w *webAPI) finish(ctx context.Context, input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return errors.New("нужен адрес, на который вас вернул Spotify (http://127.0.0.1:8888/callback?code=…)")
	}
	var pend webPending
	b, err := os.ReadFile(w.pending)
	if err != nil || json.Unmarshal(b, &pend) != nil {
		return errors.New("вход не начат (или уже закончен): сначала openmediad spotify login без аргументов")
	}
	if w.now().Sub(pend.At) > pendingTTL {
		return errors.New("со ссылки прошло больше 30 минут: начните заново (openmediad spotify login)")
	}
	code := input
	if strings.Contains(input, "?") || strings.Contains(input, "code=") {
		u, err := url.Parse(input)
		if err != nil {
			return fmt.Errorf("адрес не разобран: %w", err)
		}
		q := u.Query()
		if e := q.Get("error"); e != "" {
			return fmt.Errorf("Spotify отказал: %s", e)
		}
		if got := q.Get("state"); got != pend.State {
			return errors.New("адрес не от этой ссылки (state не совпал): начните заново (openmediad spotify login)")
		}
		if code = q.Get("code"); code == "" {
			return errors.New("в адресе нет code")
		}
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", webRedirect)
	form.Set("client_id", pend.ClientID)
	form.Set("code_verifier", pend.Verifier)
	tr, err := w.tokenRequest(ctx, form)
	if err != nil {
		return err
	}
	if tr.Refresh == "" {
		return errors.New("Spotify не выдал refresh-токен")
	}
	if err := writeSecret(w.file, webToken{Refresh: tr.Refresh, Scope: tr.Scope, ClientID: pend.ClientID, SavedAt: w.now()}); err != nil {
		return err
	}
	os.Remove(w.pending)
	w.mu.Lock()
	w.access, w.expires = tr.Access, w.now().Add(time.Duration(tr.ExpiresIn)*time.Second-time.Minute)
	w.mu.Unlock()
	return nil
}

// forget удаляет сохранённый вход.
func (w *webAPI) forget() {
	os.Remove(w.file)
	os.Remove(w.pending)
	w.mu.Lock()
	w.access, w.expires = "", time.Time{}
	w.mu.Unlock()
}

type tokenReply struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	Scope     string `json:"scope"`
	ExpiresIn int    `json:"expires_in"`
	Error     string `json:"error"`
	ErrorDesc string `json:"error_description"`
}

func (w *webAPI) tokenRequest(ctx context.Context, form url.Values) (tokenReply, error) {
	var tr tokenReply
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.auth+"/api/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tr, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := w.client().Do(req)
	if err != nil {
		return tr, fmt.Errorf("accounts.spotify.com: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	json.Unmarshal(body, &tr)
	if resp.StatusCode != http.StatusOK || tr.Access == "" {
		msg := tr.Error
		if tr.ErrorDesc != "" {
			msg += ": " + tr.ErrorDesc
		}
		if msg == "" {
			msg = resp.Status
		}
		if tr.Error == "invalid_grant" {
			msg += " (вход устарел или отозван: openmediad spotify login заново)"
		}
		return tr, fmt.Errorf("токен не получен: %s", msg)
	}
	return tr, nil
}

// accessToken отдаёт действующий токен доступа, обновляя его по refresh-токену.
func (w *webAPI) accessToken(ctx context.Context) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.access != "" && w.now().Before(w.expires) {
		return w.access, nil
	}
	return w.refreshLocked(ctx)
}

func (w *webAPI) refreshLocked(ctx context.Context) (string, error) {
	if err := w.ready(); err != nil {
		return "", err
	}
	t, _ := w.loadToken()
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.Refresh)
	form.Set("client_id", t.ClientID)
	tr, err := w.tokenRequest(ctx, form)
	if err != nil {
		w.access = ""
		return "", err
	}
	w.access, w.expires = tr.Access, w.now().Add(time.Duration(tr.ExpiresIn)*time.Second-time.Minute)
	if tr.Refresh != "" && tr.Refresh != t.Refresh { // Spotify может выдать новый refresh-токен
		t.Refresh = tr.Refresh
		if tr.Scope != "" {
			t.Scope = tr.Scope
		}
		t.SavedAt = w.now()
		writeSecret(w.file, t)
	}
	return w.access, nil
}

// trackID достаёт идентификатор из spotify:track:ID; другое (эпизод, локальный файл) лайкать нечем.
func trackID(uri string) (string, bool) {
	id, ok := strings.CutPrefix(uri, "spotify:track:")
	if !ok || id == "" || len(id) > 64 || strings.ContainsAny(id, ":?&/ ") {
		return "", false
	}
	return id, true
}

// setSaved кладёт трек в «Любимые треки» (liked) или убирает оттуда.
func (w *webAPI) setSaved(ctx context.Context, uri string, liked bool) error {
	id, ok := trackID(uri)
	if !ok {
		return fmt.Errorf("%q не трек Spotify: лайк ставить некуда", uri)
	}
	method := http.MethodPut
	if !liked {
		method = http.MethodDelete
	}
	// Основной адрес — PUT/DELETE /me/library?uris=…; старый /me/tracks?ids=… остаётся запасным, если новый не найден.
	st, body, err := w.apiCall(ctx, method, "/me/library?uris="+url.QueryEscape("spotify:track:"+id))
	if err == nil && (st == http.StatusNotFound || st == http.StatusMethodNotAllowed || st == http.StatusGone) {
		st, body, err = w.apiCall(ctx, method, "/me/tracks?ids="+url.QueryEscape(id))
	}
	if err != nil {
		return err
	}
	return apiError(st, body)
}

// isSaved говорит, лежит ли трек в «Любимых треках».
func (w *webAPI) isSaved(ctx context.Context, uri string) (bool, error) {
	id, ok := trackID(uri)
	if !ok {
		return false, fmt.Errorf("%q не трек Spotify", uri)
	}
	st, body, err := w.apiCall(ctx, http.MethodGet, "/me/library/contains?uris="+url.QueryEscape("spotify:track:"+id))
	if err == nil && (st == http.StatusNotFound || st == http.StatusMethodNotAllowed || st == http.StatusGone) {
		st, body, err = w.apiCall(ctx, http.MethodGet, "/me/tracks/contains?ids="+url.QueryEscape(id))
	}
	if err != nil {
		return false, err
	}
	if err := apiError(st, body); err != nil {
		return false, err
	}
	var res []bool
	if json.Unmarshal(body, &res) != nil || len(res) != 1 {
		return false, errors.New("неожиданный ответ Spotify")
	}
	return res[0], nil
}

// apiCall делает запрос к Web API; при 401 один раз обновляет токен и повторяет.
func (w *webAPI) apiCall(ctx context.Context, method, path string) (int, []byte, error) {
	for attempt := 0; ; attempt++ {
		tok, err := w.accessToken(ctx)
		if err != nil {
			return 0, nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, w.api+path, nil)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := w.client().Do(req)
		if err != nil {
			return 0, nil, fmt.Errorf("api.spotify.com: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			w.mu.Lock()
			w.access = ""
			w.mu.Unlock()
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			return resp.StatusCode, []byte(resp.Header.Get("Retry-After")), nil
		}
		return resp.StatusCode, body, nil
	}
}

func apiError(status int, body []byte) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized:
		return errors.New("Spotify не принял токен: openmediad spotify login заново")
	case status == http.StatusForbidden && strings.Contains(string(body), "unavailable in this country"):
		return errors.New("Web API Spotify закрыт для адреса колонки («unavailable in this country»): нужен прокси в другой стране, " +
			"openmediad config set spotify-soloist.web_proxy http://адрес:порт (или socks5://адрес:порт)")
	case status == http.StatusForbidden:
		return fmt.Errorf("Spotify отказал в доступе (403%s): у приложения нет прав на библиотеку или аккаунт не добавлен в приложение (User Management в Dashboard); попробуйте openmediad spotify login заново", apiMessage(body))
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("Spotify просит подождать (429, Retry-After %s с)", strings.TrimSpace(string(body)))
	}
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(body, &e)
	if e.Error.Message != "" {
		return fmt.Errorf("Spotify ответил %d: %s", status, e.Error.Message)
	}
	return fmt.Errorf("Spotify ответил %d", status)
}

// apiMessage достаёт текст ошибки из тела ответа Spotify (с запятой впереди, для вставки в скобки).
func apiMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error.Message == "" {
		return ""
	}
	return ": " + e.Error.Message
}

// check проверяет всю цепочку и возвращает строки отчёта; ok — лайки должны работать.
func (w *webAPI) check(ctx context.Context) (lines []string, ok bool) {
	say := func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	id := w.clientID()
	if id == "" {
		say("client_id не задан")
		return lines, false
	}
	say("client_id: задан")
	t, err := w.loadToken()
	if err != nil || t.Refresh == "" {
		say("вход: нет (openmediad spotify login)")
		return lines, false
	}
	say("вход: есть, права: %s", orDefault(t.Scope, "неизвестны"))
	if p := ""; w.proxy != nil {
		if p = w.proxy(); p != "" {
			say("прокси: %s", p)
		} else {
			say("прокси: нет (запросы идут напрямую)")
		}
	}
	w.mu.Lock()
	w.access = ""
	_, err = w.refreshLocked(ctx)
	w.mu.Unlock()
	if err != nil {
		say("токен доступа: не получен — %v", err)
		return lines, false
	}
	say("токен доступа: получен (accounts.spotify.com отвечает)")
	st, body, err := w.apiCall(ctx, http.MethodGet, "/me")
	switch {
	case err != nil:
		say("api.spotify.com: недоступен — %v", err)
		return lines, false
	case apiError(st, body) != nil:
		say("api.spotify.com: %v", apiError(st, body))
		return lines, false
	}
	say("api.spotify.com: отвечает (GET /me — %d)", st)
	return lines, true
}

func randomURLString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
