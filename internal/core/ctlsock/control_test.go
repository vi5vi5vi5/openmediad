package ctlsock

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func startServer(t *testing.T, h Handler) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ctl.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (&Server{Path: path, Handle: h}).Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	for i := 0; i < 100; i++ {
		if err := Call(path, Request{Cmd: "ping"}, nil); !errors.Is(err, ErrNotRunning) {
			return path
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("сокет не поднялся")
	return ""
}

func TestCallAndError(t *testing.T) {
	path := startServer(t, func(ctx context.Context, req Request, send func(any) error) error {
		switch req.Cmd {
		case "status":
			return send(map[string]any{"pid": 42, "detail": req.Detail})
		case "ping":
			return send("pong")
		}
		return errors.New("неизвестная команда " + req.Cmd)
	})
	var st struct {
		PID    int  `json:"pid"`
		Detail bool `json:"detail"`
	}
	if err := Call(path, Request{Cmd: "status", Detail: true}, &st); err != nil || st.PID != 42 || !st.Detail {
		t.Fatalf("status: %v %+v", err, st)
	}
	if err := Call(path, Request{Cmd: "нет"}, nil); err == nil || err.Error() != "неизвестная команда нет" {
		t.Fatalf("ошибка обработчика должна дойти до клиента: %v", err)
	}
}

func TestStreamStopsWhenClientLeaves(t *testing.T) {
	stopped := make(chan struct{})
	path := startServer(t, func(ctx context.Context, req Request, send func(any) error) error {
		if req.Cmd == "ping" {
			return send("pong")
		}
		defer close(stopped)
		for i := 0; ; i++ {
			if err := send(i); err != nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Millisecond):
			}
		}
	})
	var got []int
	err := Stream(path, Request{Cmd: "follow"}, func(d json.RawMessage) bool {
		var v int
		json.Unmarshal(d, &v)
		got = append(got, v)
		return len(got) < 3
	})
	if err != nil || len(got) != 3 || got[2] != 2 {
		t.Fatalf("поток: %v %v", err, got)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("обработчик потока не остановился после ухода клиента")
	}
}

func TestNotRunning(t *testing.T) {
	if err := Call(filepath.Join(t.TempDir(), "нет.sock"), Request{Cmd: "status"}, nil); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("ждали ErrNotRunning, получили %v", err)
	}
}

func TestSecondDaemonRefused(t *testing.T) {
	path := startServer(t, func(ctx context.Context, req Request, send func(any) error) error { return send("pong") })
	err := (&Server{Path: path, Handle: nil}).Run(context.Background())
	if err == nil {
		t.Fatal("второй демон на том же сокете должен отказаться запускаться")
	}
}
