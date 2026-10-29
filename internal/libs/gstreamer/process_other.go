//go:build !linux

package gstreamer

import (
	"errors"
	"io"
	"os"
)

// Process — заглушка вне колонки: GStreamer есть только в прошивке (Linux), тесты подставляют свои декодеры.
type Process struct {
	In  io.WriteCloser
	Out *os.File
}

// ErrUnavailable — GStreamer колонки на этой системе недоступен.
var ErrUnavailable = errors.New("GStreamer колонки (maind --gst-launch) доступен только на колонке (Linux)")

func Start(pipeline string, withStdin bool) (*Process, error) { return nil, ErrUnavailable }
func (p *Process) Done() <-chan struct{}                      { return nil }
func (p *Process) Wait() error                                { return ErrUnavailable }
func (p *Process) Tail() string                               { return "" }
func (p *Process) Kill()                                      {}
func (p *Process) Close() error                               { return nil }
