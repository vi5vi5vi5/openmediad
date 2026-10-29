package btin

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"openmediad/internal/core/speaker/aplay"
)

// errStuck — arecord не завершился после SIGINT. Процесс оставлен жить: SIGKILL не используется (как у aplay).
var errStuck = errors.New("btin: arecord не завершается; SIGKILL не используется")

// tailBuf хранит последние 512 байт вывода процесса (для сообщений об ошибках).
type tailBuf struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 512 {
		t.b = t.b[len(t.b)-512:]
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

// recorder — запущенный arecord, отдающий PCM S16LE в свой stdout (звук телефона из PCM BlueALSA). Как и aplay, это
// системная 32-битная утилита: ALSA-плагин BlueALSA в прошивке 32-битный. Останавливается SIGINT: arecord сам закрывает
// PCM; SIGKILL не используется (тот же принцип, что у aplay).
type recorder struct {
	cmd     *exec.Cmd
	out     io.ReadCloser
	done    chan struct{}
	waitErr error
	stderr  tailBuf
}

// startRecord запускает arecord с параметрами cfg (Path по умолчанию /usr/bin/arecord, Device обязателен). Аргументы командной
// строки у arecord и aplay одни и те же, поэтому их собирает aplay.Config.
func startRecord(cfg aplay.Config) (*recorder, error) {
	if cfg.Path == "" {
		cfg.Path = "/usr/bin/arecord"
	}
	r := &recorder{done: make(chan struct{})}
	r.cmd = exec.Command(cfg.Path, cfg.Args()...)
	r.cmd.Stderr = &r.stderr
	out, err := r.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	r.out = out
	if err := r.cmd.Start(); err != nil {
		return nil, fmt.Errorf("alsa: запуск %s: %w", cfg.Path, err)
	}
	go func() {
		r.waitErr = r.cmd.Wait()
		close(r.done)
	}()
	return r, nil
}

// Read отдаёт записанный PCM; блокируется, пока данных нет. io.EOF — arecord завершился.
func (r *recorder) Read(p []byte) (int, error) { return r.out.Read(p) }

// Done закрывается, когда процесс arecord завершился.
func (r *recorder) Done() <-chan struct{} { return r.done }

// Stop останавливает запись (SIGINT) и ждёт завершения до grace; иначе errStuck (процесс оставлен жить).
func (r *recorder) Stop(grace time.Duration) error {
	select {
	case <-r.done:
		return r.result()
	default:
	}
	if err := r.cmd.Process.Signal(os.Interrupt); err != nil {
		select {
		case <-r.done:
			return r.result()
		default:
			return fmt.Errorf("alsa: SIGINT: %w", err)
		}
	}
	select {
	case <-r.done:
		return r.result()
	case <-time.After(grace):
		return errStuck
	}
}

func (r *recorder) result() error {
	if r.waitErr == nil {
		return nil
	}
	if s := r.stderr.String(); s != "" {
		return fmt.Errorf("alsa: arecord: %w: %s", r.waitErr, s)
	}
	return fmt.Errorf("alsa: arecord: %w", r.waitErr)
}
