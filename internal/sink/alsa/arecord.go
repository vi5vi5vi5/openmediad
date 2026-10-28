package alsa

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// Recorder — запущенный arecord, отдающий PCM S16LE в свой stdout (звук телефона из PCM BlueALSA). Как и aplay, это
// системная 32-битная утилита: ALSA-плагин BlueALSA в прошивке 32-битный. Останавливается SIGINT: arecord сам закрывает
// PCM; SIGKILL не используется (тот же принцип, что у aplay).
type Recorder struct {
	cmd     *exec.Cmd
	out     io.ReadCloser
	done    chan struct{}
	waitErr error
	stderr  tail
}

// StartRecord запускает arecord с параметрами cfg (Path по умолчанию /usr/bin/arecord, Device обязателен).
func StartRecord(cfg Config) (*Recorder, error) {
	if cfg.Path == "" {
		cfg.Path = "/usr/bin/arecord"
	}
	cfg = cfg.withDefaults()
	r := &Recorder{done: make(chan struct{})}
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
func (r *Recorder) Read(p []byte) (int, error) { return r.out.Read(p) }

// Done закрывается, когда процесс arecord завершился.
func (r *Recorder) Done() <-chan struct{} { return r.done }

// Stop останавливает запись (SIGINT) и ждёт завершения до grace; иначе ErrStuck (процесс оставлен жить).
func (r *Recorder) Stop(grace time.Duration) error {
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
		return ErrStuck
	}
}

func (r *Recorder) result() error {
	if r.waitErr == nil {
		return nil
	}
	if s := r.stderr.String(); s != "" {
		return fmt.Errorf("alsa: arecord: %w: %s", r.waitErr, s)
	}
	return fmt.Errorf("alsa: arecord: %w", r.waitErr)
}
