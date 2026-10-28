//go:build linux

package gst

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Process — запущенный `maind --gst-launch`. Out — PCM из третьего дескриптора процесса; In — его stdin (если запрошен).
type Process struct {
	In  io.WriteCloser // nil, если stdin не нужен (конвейер читает файл сам)
	Out *os.File

	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error
	once    sync.Once
	log     tail
}

// Start запускает конвейер; pipeline должен заканчиваться выводом в fd 3 (см. RawOut). withStdin — подключить stdin
// процесса (для `fdsrc fd=0`).
func Start(pipeline string, withStdin bool) (*Process, error) {
	rd, wr, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	p := &Process{Out: rd, done: make(chan struct{})}
	p.cmd = exec.Command(MaindPath, "--gst-launch", pipeline)
	p.cmd.Stdout = &p.log
	p.cmd.Stderr = &p.log
	p.cmd.ExtraFiles = []*os.File{wr} // станет fd 3 в дочернем процессе
	if withStdin {
		if p.In, err = p.cmd.StdinPipe(); err != nil {
			rd.Close()
			wr.Close()
			return nil, err
		}
	}
	if err := p.cmd.Start(); err != nil {
		rd.Close()
		wr.Close()
		return nil, fmt.Errorf("запуск %s: %w", MaindPath, err)
	}
	wr.Close() // у нас остаётся только читающий конец; EOF придёт, когда процесс закроет свой
	go func() {
		p.waitErr = p.cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// Done закрывается, когда процесс завершился.
func (p *Process) Done() <-chan struct{} { return p.done }

// Wait ждёт завершения процесса и возвращает его ошибку (с хвостом журнала, если процесс завершился неудачно).
func (p *Process) Wait() error {
	<-p.done
	if p.waitErr != nil {
		return fmt.Errorf("%s: %w: %s", MaindPath, p.waitErr, p.log.String())
	}
	return nil
}

// Tail — хвост журнала процесса.
func (p *Process) Tail() string { return p.log.String() }

// Kill добивает процесс сразу (зависший конвейер, слишком длинный файл).
func (p *Process) Kill() { p.cmd.Process.Kill() }

// Close закрывает вход и просит процесс завершиться (SIGTERM); если он не слушается, добивает SIGKILL. Ждёт завершения.
func (p *Process) Close() error {
	p.once.Do(func() {
		if p.In != nil {
			p.In.Close()
		}
		select {
		case <-p.done:
		case <-time.After(200 * time.Millisecond):
			p.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-p.done:
			case <-time.After(2 * time.Second):
				p.cmd.Process.Kill()
				<-p.done
			}
		}
		p.Out.Close()
	})
	return nil
}
