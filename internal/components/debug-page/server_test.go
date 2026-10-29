package debugpage

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openmediad/internal/core/journal"
)

func newTestServer(t *testing.T, cors bool) (*journal.Journal, *httptest.Server) {
	h := &journal.Journal{}
	s := &Server{Hub: h, CORS: cors, State: func() any { return map[string]any{"mode": "тест"} }}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return h, ts
}

func TestHTTPEndpoints(t *testing.T) {
	h, ts := newTestServer(t, false)
	h.Log(journal.Info, "shim", "первая", "пояснение", map[string]any{"k": "v"})
	h.Log(journal.Warn, "music", "вторая", "", nil)

	resp, err := http.Get(ts.URL + "/")
	if err != nil || resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("страница: %v %v", resp, err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "/api/stream") {
		t.Fatal("страница должна подключаться к /api/stream")
	}
	if r, _ := http.Get(ts.URL + "/нет"); r.StatusCode != 404 {
		t.Fatalf("лишний путь: %d", r.StatusCode)
	}

	var es []journal.Entry
	resp, _ = http.Get(ts.URL + "/api/log?since=1")
	json.NewDecoder(resp.Body).Decode(&es)
	if len(es) != 1 || es[0].Msg != "вторая" {
		t.Fatalf("/api/log?since=1: %+v", es)
	}
	resp, _ = http.Get(ts.URL + "/api/log?format=ndjson")
	nd, _ := io.ReadAll(resp.Body)
	if n := strings.Count(strings.TrimSpace(string(nd)), "\n") + 1; n != 2 {
		t.Fatalf("ndjson: %d строк, ждали 2", n)
	}

	var st map[string]any
	resp, _ = http.Get(ts.URL + "/api/state")
	json.NewDecoder(resp.Body).Decode(&st)
	if st["daemon"].(map[string]any)["mode"] != "тест" || st["uptime_s"] == nil {
		t.Fatalf("/api/state: %v", st)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS по умолчанию выключен")
	}
	if r, _ := http.Post(ts.URL+"/api/log", "text/plain", nil); r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("запись через порт невозможна, а POST вернул %d", r.StatusCode)
	}
}

func TestCORSOnlyWhenEnabled(t *testing.T) {
	_, ts := newTestServer(t, true)
	resp, _ := http.Get(ts.URL + "/api/state")
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("с CORS=true заголовок должен быть")
	}
}

func TestStreamSendsBacklogThenLive(t *testing.T) {
	h, ts := newTestServer(t, false)
	h.Log(journal.Info, "shim", "старая", "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	read := func() journal.Entry {
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") {
				var e journal.Entry
				json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e)
				return e
			}
		}
	}
	if e := read(); e.Msg != "старая" {
		t.Fatalf("сначала накопленное, а пришло %q", e.Msg)
	}
	go func() { time.Sleep(100 * time.Millisecond); h.Log(journal.Info, "shim", "новая", "", nil) }()
	if e := read(); e.Msg != "новая" {
		t.Fatalf("затем новое, а пришло %q", e.Msg)
	}
}

func TestStreamLimit(t *testing.T) {
	_, ts := newTestServer(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var bodies []io.Closer
	for i := 0; i < maxStreams; i++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/stream", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("поток %d: %v", i, err)
		}
		bodies = append(bodies, resp.Body)
	}
	time.Sleep(100 * time.Millisecond)
	resp, err := http.Get(ts.URL + "/api/stream")
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("лишний слушатель должен получить 503: %v %v", resp, err)
	}
	for _, b := range bodies {
		b.Close()
	}
}
