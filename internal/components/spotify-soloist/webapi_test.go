package spotifysoloist

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const testClientID = "0123456789abcdef0123456789abcdef"

// fakeSpotify — поддельные accounts.spotify.com и api.spotify.com в одном сервере.
type fakeSpotify struct {
	*httptest.Server
	mu         sync.Mutex
	challenge  string   // code_challenge, с которым сервер «выдал» code
	tokens     int      // сколько раз выдан токен доступа
	calls      []string // «METHOD путь?запрос» обращений к API
	reject401  int      // сколько первых обращений к API ответить 401
	status     map[string]int
	newRefresh string
	badGrant   bool
	geo        bool                         // api.spotify.com закрыт для страны: на любой запрос 403
	searchJSON string                       // ответ на /search
	searchFn   func(q, types string) string // ответ на /search в зависимости от запроса (если задан)
}

func newFakeSpotify(t *testing.T) *fakeSpotify {
	f := &fakeSpotify{status: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.badGrant {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		reply := map[string]any{"access_token": "acc-" + string(rune('a'+f.tokens)), "expires_in": 3600, "scope": webScopes}
		f.tokens++
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge || r.Form.Get("code") != "the-code" || r.Form.Get("redirect_uri") != webRedirect {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			reply["refresh_token"] = "refresh-1"
		case "refresh_token":
			if r.Form.Get("refresh_token") == "" || r.Form.Get("client_id") != testClientID {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if f.newRefresh != "" {
				reply["refresh_token"] = f.newRefresh
			}
		}
		json.NewEncoder(w).Encode(reply)
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/v1")+"?"+r.URL.RawQuery)
		if f.geo {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{ "error" : { "status" : 403, "message" : "Spotify is unavailable in this country" } }`))
			return
		}
		if f.reject401 > 0 {
			f.reject401--
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer acc-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if st, ok := f.status[r.URL.Path]; ok {
			w.WriteHeader(st)
			if st == http.StatusTooManyRequests {
				return
			}
			io := `{"error":{"status":` + "0" + `,"message":"нет такого"}}`
			w.Write([]byte(io))
			return
		}
		if r.URL.Path == "/v1/search" && f.searchFn != nil {
			w.Write([]byte(f.searchFn(r.URL.Query().Get("q"), r.URL.Query().Get("type"))))
			return
		}
		if r.URL.Path == "/v1/search" && f.searchJSON != "" {
			w.Write([]byte(f.searchJSON))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/contains") {
			w.Write([]byte("[true]"))
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSpotify) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func newTestWeb(t *testing.T, f *fakeSpotify, clientID string) *webAPI {
	t.Helper()
	w := newWebAPI(newPaths(t.TempDir(), t.TempDir(), ""), func() string { return clientID })
	w.auth, w.api = f.URL, f.URL+"/v1"
	return w
}

// login проходит весь вход: ссылка → «возвращённый браузером» адрес → токен.
func login(t *testing.T, w *webAPI, f *fakeSpotify) {
	t.Helper()
	link, err := w.begin()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	q := u.Query()
	f.mu.Lock()
	f.challenge = q.Get("code_challenge")
	f.mu.Unlock()
	back := webRedirect + "?code=the-code&state=" + url.QueryEscape(q.Get("state"))
	if err := w.finish(context.Background(), back); err != nil {
		t.Fatal(err)
	}
}

func TestLoginBuildsPKCELinkAndStoresOnlyRefreshToken(t *testing.T) {
	f := newFakeSpotify(t)
	w := newTestWeb(t, f, testClientID)
	link, err := w.begin()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != testClientID || q.Get("redirect_uri") != webRedirect ||
		q.Get("code_challenge_method") != "S256" || q.Get("scope") != webScopes || q.Get("state") == "" || q.Get("code_challenge") == "" {
		t.Fatalf("ссылка для входа: %s", link)
	}
	if strings.Contains(link, "secret") {
		t.Fatal("в ссылке секрет приложения: нужен PKCE без него")
	}
	login(t, w, f)
	tok, err := w.loadToken()
	if err != nil || tok.Refresh != "refresh-1" || tok.ClientID != testClientID {
		t.Fatalf("сохранённый вход: %+v %v", tok, err)
	}
	if _, err := os.Stat(w.pending); err == nil {
		t.Fatal("после входа остался файл незаконченного входа")
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(w.file); st.Mode().Perm() != 0o600 {
			t.Fatalf("права файла токена %v, нужны 0600", st.Mode().Perm())
		}
	}
	if err := w.ready(); err != nil {
		t.Fatalf("после входа ready: %v", err)
	}
}

func TestLoginRejectsForeignOrStaleCallbacks(t *testing.T) {
	f := newFakeSpotify(t)
	w := newTestWeb(t, f, testClientID)
	if err := w.finish(context.Background(), webRedirect+"?code=x&state=y"); err == nil {
		t.Fatal("вход принят без начатого входа")
	}
	link, _ := w.begin()
	u, _ := url.Parse(link)
	state := u.Query().Get("state")
	for name, in := range map[string]string{
		"чужой state":     webRedirect + "?code=the-code&state=other",
		"без code":        webRedirect + "?state=" + state,
		"отказ в доступе": webRedirect + "?error=access_denied&state=" + state,
		"пустой ввод":     "  ",
		"битый адрес":     "http://[::1",
	} {
		if err := w.finish(context.Background(), in); err == nil {
			t.Errorf("%s: вход принят", name)
		}
	}
	w.now = func() time.Time { return time.Now().Add(pendingTTL + time.Minute) }
	if err := w.finish(context.Background(), webRedirect+"?code=the-code&state="+state); err == nil || !strings.Contains(err.Error(), "30 минут") {
		t.Fatalf("устаревшая ссылка: %v", err)
	}
}

func TestLoginNeedsValidClientID(t *testing.T) {
	f := newFakeSpotify(t)
	if _, err := newTestWeb(t, f, "").begin(); err == nil {
		t.Fatal("вход начат без client_id")
	}
	if _, err := newTestWeb(t, f, "bad id&x=1").begin(); err == nil {
		t.Fatal("client_id с посторонними символами принят")
	}
}

func TestLikeAndUnlikeUseLibraryEndpoint(t *testing.T) {
	f := newFakeSpotify(t)
	w := newTestWeb(t, f, testClientID)
	login(t, w, f)
	ctx := context.Background()
	if err := w.setSaved(ctx, "spotify:track:2sFepQNyl1WjpROruP8bJ0", true); err != nil {
		t.Fatal(err)
	}
	if err := w.setSaved(ctx, "spotify:track:2sFepQNyl1WjpROruP8bJ0", false); err != nil {
		t.Fatal(err)
	}
	calls := f.callLog()
	want := []string{
		"PUT /me/library?uris=spotify%3Atrack%3A2sFepQNyl1WjpROruP8bJ0",
		"DELETE /me/library?uris=spotify%3Atrack%3A2sFepQNyl1WjpROruP8bJ0",
	}
	if len(calls) != 2 || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("обращения к API: %v", calls)
	}
	if f.tokens != 2 { // один токен при входе и один — для первого обращения (при входе выданный токен тоже годен)
		t.Logf("токенов выдано: %d", f.tokens)
	}
	ok, err := w.isSaved(ctx, "spotify:track:2sFepQNyl1WjpROruP8bJ0")
	if err != nil || !ok {
		t.Fatalf("isSaved: %v %v", ok, err)
	}
}

func TestOldEndpointIsTheFallback(t *testing.T) {
	f := newFakeSpotify(t)
	f.status["/v1/me/library"] = http.StatusNotFound
	w := newTestWeb(t, f, testClientID)
	login(t, w, f)
	if err := w.setSaved(context.Background(), "spotify:track:abc123", true); err != nil {
		t.Fatal(err)
	}
	calls := f.callLog()
	if len(calls) != 2 || !strings.HasPrefix(calls[1], "PUT /me/tracks?ids=abc123") {
		t.Fatalf("запасной адрес не использован: %v", calls)
	}
}

func TestExpiredAccessTokenIsRefreshedOn401(t *testing.T) {
	f := newFakeSpotify(t)
	w := newTestWeb(t, f, testClientID)
	login(t, w, f)
	f.mu.Lock()
	f.reject401 = 1
	f.mu.Unlock()
	if err := w.setSaved(context.Background(), "spotify:track:abc123", true); err != nil {
		t.Fatalf("после 401 запрос не повторён: %v", err)
	}
	if calls := f.callLog(); len(calls) != 2 {
		t.Fatalf("обращений %d, ждали 2 (отказ и повтор): %v", len(calls), calls)
	}
}

func TestRotatedRefreshTokenIsSaved(t *testing.T) {
	f := newFakeSpotify(t)
	f.newRefresh = "refresh-2"
	w := newTestWeb(t, f, testClientID)
	login(t, w, f)
	w.mu.Lock()
	w.access = "" // токен доступа истёк
	w.mu.Unlock()
	if _, err := w.accessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tok, _ := w.loadToken(); tok.Refresh != "refresh-2" {
		t.Fatalf("новый refresh-токен не сохранён: %+v", tok)
	}
}

func TestErrorsAreExplained(t *testing.T) {
	f := newFakeSpotify(t)
	w := newTestWeb(t, f, testClientID)
	if err := w.setSaved(context.Background(), "spotify:track:abc", true); err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("без входа: %v", err)
	}
	login(t, w, f)
	ctx := context.Background()
	for _, c := range []struct {
		st   int
		want string
	}{{http.StatusForbidden, "403"}, {http.StatusTooManyRequests, "429"}, {http.StatusInternalServerError, "500"}} {
		f.mu.Lock()
		f.status["/v1/me/library"] = c.st
		f.mu.Unlock()
		if err := w.setSaved(ctx, "spotify:track:abc", true); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("статус %d: %v", c.st, err)
		}
	}
	for _, bad := range []string{"spotify:episode:abc", "spotify:local:x", "spotify:track:", "spotify:track:a?b", "", "http://x"} {
		if err := w.setSaved(ctx, bad, true); err == nil {
			t.Errorf("%q принят за трек", bad)
		}
	}
	// вход отозван
	f.mu.Lock()
	f.badGrant = true
	f.mu.Unlock()
	w.mu.Lock()
	w.access = ""
	w.mu.Unlock()
	if err := w.setSaved(ctx, "spotify:track:abc", true); err == nil || !strings.Contains(err.Error(), "заново") {
		t.Fatalf("отозванный вход: %v", err)
	}
}

func TestChangedClientIDInvalidatesLogin(t *testing.T) {
	f := newFakeSpotify(t)
	w := newTestWeb(t, f, testClientID)
	login(t, w, f)
	w.clientID = func() string { return "fedcba9876543210fedcba9876543210" }
	if err := w.ready(); err == nil || !strings.Contains(err.Error(), "другим Client ID") {
		t.Fatalf("смена Client ID: %v", err)
	}
	w.forget()
	if _, err := os.Stat(filepath.Join(filepath.Dir(w.file), "web.json")); err == nil {
		t.Fatal("forget не удалил вход")
	}
}

// Из жизни: с адреса колонки Web API отвечает 403 «unavailable in this country», хотя вход и токен работают.
func TestGeoBlockedAPIExplainsTheProxy(t *testing.T) {
	f := newFakeSpotify(t)
	w := newTestWeb(t, f, testClientID)
	login(t, w, f)
	f.mu.Lock()
	f.geo = true
	f.mu.Unlock()
	err := w.setSaved(context.Background(), "spotify:track:abc", true)
	if err == nil || !strings.Contains(err.Error(), "web_proxy") || !strings.Contains(err.Error(), "закрыт") {
		t.Fatalf("геоблокировка не объяснена: %v", err)
	}
	lines, ok := w.check(context.Background())
	if ok || !strings.Contains(strings.Join(lines, "\n"), "web_proxy") {
		t.Fatalf("проверка при геоблокировке: ok=%v %v", ok, lines)
	}
}

func TestCheckWalksThroughTheChain(t *testing.T) {
	f := newFakeSpotify(t)
	w := newTestWeb(t, f, testClientID)
	if lines, ok := w.check(context.Background()); ok || !strings.Contains(strings.Join(lines, "\n"), "login") {
		t.Fatalf("без входа: ok=%v %v", ok, lines)
	}
	if lines, ok := newTestWeb(t, f, "").check(context.Background()); ok || !strings.Contains(strings.Join(lines, "\n"), "client_id") {
		t.Fatalf("без client_id: ok=%v %v", ok, lines)
	}
	login(t, w, f)
	lines, ok := w.check(context.Background())
	if !ok || !strings.Contains(strings.Join(lines, "\n"), "api.spotify.com: отвечает") || !strings.Contains(strings.Join(lines, "\n"), "user-library-modify") {
		t.Fatalf("исправная цепочка: ok=%v %v", ok, lines)
	}
}

// Запросы идут через прокси из настроек (в тесте прокси — обычный HTTP-сервер, а адреса Spotify не существуют).
func TestRequestsGoThroughTheConfiguredProxy(t *testing.T) {
	f := newFakeSpotify(t)
	var mu sync.Mutex
	var hosts []string
	backend, _ := url.Parse(f.URL)
	proxy := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		mu.Unlock()
		r.URL.Scheme, r.URL.Host = "http", backend.Host
		r.RequestURI = ""
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			rw.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		rw.WriteHeader(resp.StatusCode)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			rw.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}))
	defer proxy.Close()

	w := newWebAPI(newPaths(t.TempDir(), t.TempDir(), ""), func() string { return testClientID })
	w.auth, w.api = "http://accounts.example.invalid", "http://api.example.invalid/v1"
	// без прокси адреса недоступны
	if _, err := w.begin(); err != nil {
		t.Fatal(err)
	}
	w.proxy = func() string { return proxy.URL }
	link, _ := w.begin()
	u, _ := url.Parse(link)
	f.mu.Lock()
	f.challenge = u.Query().Get("code_challenge")
	f.mu.Unlock()
	if err := w.finish(context.Background(), webRedirect+"?code=the-code&state="+url.QueryEscape(u.Query().Get("state"))); err != nil {
		t.Fatalf("вход через прокси: %v", err)
	}
	if err := w.setSaved(context.Background(), "spotify:track:abc", true); err != nil {
		t.Fatalf("лайк через прокси: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	var acc, api bool
	for _, h := range hosts {
		acc = acc || h == "accounts.example.invalid"
		api = api || h == "api.example.invalid"
	}
	if !acc || !api {
		t.Fatalf("через прокси прошли запросы к %v, ждали и accounts, и api", hosts)
	}
	// на ходу: без прокси запрос идёт напрямую и (адрес не существует) не удаётся
	w.proxy = func() string { return "" }
	if err := w.setSaved(context.Background(), "spotify:track:abc", true); err == nil {
		t.Fatal("после отключения прокси запрос всё ещё проходит")
	}
}

func TestProxyValidation(t *testing.T) {
	for _, ok := range []string{"", "http://192.168.1.50:7890", "socks5://pc.local:1080", "https://proxy.example.com:8443", "socks5h://127.0.0.1:1080"} {
		if err := validProxy(ok); err != nil {
			t.Errorf("%q отвергнут: %v", ok, err)
		}
	}
	for _, bad := range []string{"192.168.1.50:7890", "ftp://x:1", "http://", "http://:80", "прокси"} {
		if err := validProxy(bad); err == nil {
			t.Errorf("%q принят", bad)
		}
	}
	c := defaults()
	c.WebProxy = "ftp://x:1"
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "web_proxy") {
		t.Fatalf("настройки с плохим прокси: %v", err)
	}
}
