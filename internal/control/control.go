// Package control — канал команд между CLI (`openmediad status`, `logs`…) и демоном: unix-сокет и строки JSON.
//
// Запрос — одна строка `{"cmd":"status"}`; ответ — одна или несколько строк `{"ok":true,"data":…}` (несколько — у потока
// журнала, пока клиент не отключится). Без net/http: на колонке это лишний вес.
package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// RunDir — каталог рабочих файлов демона (это tmpfs, то есть ОЗУ: только маленькие файлы). Другой — OPENMEDIAD_RUN.
func RunDir() string {
	if d := os.Getenv("OPENMEDIAD_RUN"); d != "" {
		return d
	}
	return "/tmp/openmediad"
}

// SocketPath — сокет демона.
func SocketPath() string { return filepath.Join(RunDir(), "ctl.sock") }

// Request — команда демону.
type Request struct {
	Cmd    string `json:"cmd"`              // status | logs | follow | reload
	Since  uint64 `json:"since,omitempty"`  // logs, follow: записи после этой
	Limit  int    `json:"limit,omitempty"`  // logs: не больше стольких последних
	Level  string `json:"level,omitempty"`  // logs, follow: не ниже этого уровня
	Detail bool   `json:"detail,omitempty"` // status: с плеерами и треками
}

// Response — ответ демона (одна строка).
type Response struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Handler обрабатывает запрос; send отправляет клиенту очередную строку данных (ошибка — клиент отключился).
type Handler func(ctx context.Context, req Request, send func(v any) error) error

// Server — сокет команд.
type Server struct {
	Path   string
	Handle Handler
	Logf   func(string, ...any)
}

// Run слушает сокет, пока не отменён ctx. Старый сокет (от упавшего демона) удаляется; права — только владельцу (root).
func (s *Server) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	if c, err := net.DialTimeout("unix", s.Path, 200*time.Millisecond); err == nil {
		c.Close()
		return fmt.Errorf("сокет %s занят: демон уже запущен", s.Path)
	}
	os.Remove(s.Path)
	ln, err := net.Listen("unix", s.Path)
	if err != nil {
		return err
	}
	os.Chmod(s.Path, 0o600)
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	defer os.Remove(s.Path)
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.serve(ctx, c)
	}
}

func (s *Server) serve(ctx context.Context, c net.Conn) {
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return
	}
	c.SetReadDeadline(time.Time{})
	var req Request
	enc := json.NewEncoder(c)
	if err := json.Unmarshal(line, &req); err != nil {
		enc.Encode(Response{Error: "непонятный запрос: " + err.Error()})
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { // клиент отключился — прекратить поток (журнал -f)
		var b [1]byte
		c.Read(b[:])
		cancel()
	}()
	send := func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return enc.Encode(Response{OK: true, Data: data})
	}
	if err := s.Handle(cctx, req, send); err != nil {
		enc.Encode(Response{Error: err.Error()})
	}
}

// ErrNotRunning — демон не отвечает (не запущен).
var ErrNotRunning = errors.New("демон openmediad не запущен (сокет не отвечает)")

// Call отправляет запрос и разбирает первую строку ответа в v.
func Call(path string, req Request, v any) error {
	return Stream(path, req, func(data json.RawMessage) bool {
		if v != nil {
			json.Unmarshal(data, v)
		}
		return false
	})
}

// Stream отправляет запрос и отдаёт строки данных fn, пока она возвращает true и демон пишет.
func Stream(path string, req Request, fn func(json.RawMessage) bool) error {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return ErrNotRunning
	}
	defer c.Close()
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return err
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var r Response
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return fmt.Errorf("непонятный ответ демона: %w", err)
		}
		if !r.OK {
			return errors.New(r.Error)
		}
		if !fn(r.Data) {
			return nil
		}
	}
	return sc.Err()
}
