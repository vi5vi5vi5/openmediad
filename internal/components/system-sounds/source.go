package systemsounds

import (
	"context"
	"fmt"
	"io"
	"sync"

	"openmediad/internal/core/audio"
)

// Source — звонок: файл, который играет по кругу без разрыва, пока поток не закроют; с Once — один раз.
type Source struct {
	Once bool // сыграть файл один раз и закончить (io.EOF): одноразовый системный звук

	lib  *Library
	path string

	mu   sync.Mutex
	clip *Clip
	off  int
}

// NewSource готовит звонок; раскодирование — в Open (вывод в это время уже поднимается).
func NewSource(lib *Library, path string) *Source { return &Source{lib: lib, path: path} }

// Format — стерео 48 кГц.
func (s *Source) Format() audio.Format { return audio.Stereo48 }

// Open раскодирует файл (или берёт из кэша).
func (s *Source) Open(context.Context) (audio.Info, error) {
	c, err := s.lib.Clip(s.path)
	if err != nil {
		return audio.Info{}, fmt.Errorf("раскодирование %s: %w", s.path, err)
	}
	s.mu.Lock()
	s.clip = c
	s.mu.Unlock()
	return audio.Info{Duration: c.Duration(), Bitrate: int(c.Bitrate)}, nil
}

// Read отдаёт звук по кругу: после конца файла — снова с начала. С Once после конца файла — io.EOF.
func (s *Source) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pcm := s.clip.PCM
	if s.Once && s.off == len(pcm) {
		return 0, io.EOF
	}
	n := copy(p, pcm[s.off:])
	if s.off += n; s.off == len(pcm) && !s.Once {
		s.off = 0
	}
	return n, nil
}

// Close ничего не держит.
func (s *Source) Close() error { return nil }
