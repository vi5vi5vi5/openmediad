// Package device — колонка ли это и какая: модель и прошивка (из приветствия aliced), права root.
//
// Команды, меняющие систему, работают только на колонке и от root; на компьютере они честно говорят «это для колонки».
package device

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"openmediad/internal/core/platform/layout"
	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/wire"
)

// Maind — штатный бинарник Quasar колонки (в нём живут platformd, mediad и GStreamer).
const Maind = "/system/vendor/quasar/maind"

// Model — модель, под которую сделан openmediad (Яндекс Станция Мини 3).
const Model = "bergamot"

// KnownFirmware — прошивки, на которых протокол проверен. Другая — предупреждение: протокол мог измениться.
var KnownFirmware = []string{"0.369.1.50.4794652521.20261021.117"}

// ErrNotSpeaker — это не колонка (нет maind или не Linux).
var ErrNotSpeaker = errors.New("это не колонка: нет " + Maind)

// Check — похоже ли окружение на колонку (Linux и штатный maind на месте).
func Check() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("это не колонка: %s, а нужен Linux", runtime.GOOS)
	}
	if _, err := os.Stat(layout.Root + Maind); err != nil {
		return ErrNotSpeaker
	}
	return nil
}

// RequireRoot — ошибка, если не root.
func RequireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("нужны права root")
	}
	return nil
}

// Info — сведения о колонке из приветствия aliced.
type Info struct {
	Model    string
	Firmware string
}

// KnownModel — та ли это модель.
func (i Info) KnownModel() bool { return i.Model == Model }

// KnownFirmware — проверена ли прошивка.
func (i Info) KnownFirmware() bool {
	for _, f := range KnownFirmware {
		if i.Firmware == f {
			return true
		}
	}
	return false
}

func (i Info) String() string {
	if i.Model == "" {
		return "неизвестно"
	}
	return i.Model + ", прошивка " + i.Firmware
}

// AlicedAddr — откуда читать приветствие.
var AlicedAddr = "127.0.0.1:9875"

// Identify подключается к aliced и читает его приветствие (тип 207): модель и версию прошивки. Нужна работающая
// платформа (после загрузки — через 20–60 с).
func Identify(timeout time.Duration) (Info, error) {
	c, err := net.DialTimeout("tcp", AlicedAddr, timeout)
	if err != nil {
		return Info{}, fmt.Errorf("aliced не отвечает (%s): платформа ещё загружается?", AlicedAddr)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(timeout))
	fr := wire.NewFrameReader(c)
	for {
		frame, err := fr.Next()
		if err != nil {
			return Info{}, fmt.Errorf("приветствие aliced не пришло: %w", err)
		}
		m, err := wire.Unwrap(frame)
		if err != nil || m.Type != wire.TypeHandshake {
			continue
		}
		_, ver, _, model := msg.Handshake(m.Body).Device()
		return Info{Model: strings.TrimSpace(model), Firmware: strings.TrimSpace(ver)}, nil
	}
}
