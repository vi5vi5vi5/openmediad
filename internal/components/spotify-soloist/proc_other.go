//go:build !linux

package spotifysoloist

import (
	"errors"
	"syscall"
)

// Вне Linux (проверки на компьютере) процесс-надзор работает без привязок к ядру колонки.

func sysProcAttr() *syscall.SysProcAttr { return nil }

func lockThread() {}

func setNice(pid, nice int) error { return nil }

func threadTicks(pid int) (map[int]uint64, error) { return nil, errors.New("только Linux") }

func killOrphan(pidFile, soloistPath string) bool { return false }
