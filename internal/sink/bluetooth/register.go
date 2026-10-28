package bluetooth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"openmediad/internal/config"
	"openmediad/internal/quasar/control"
	"openmediad/internal/registry"
	"openmediad/internal/system/alsactl"
	"openmediad/internal/system/bluez"
	"openmediad/internal/system/proc"
)

// Config — секция [sink.bluetooth].
type Config struct {
	config.Toggle
	Device    string        `conf:"device"`    // адрес наушников; пусто — любые подключённые
	Aplay     string        `conf:"aplay"`     // системный aplay
	Volume    string        `conf:"volume"`    // speaker | headphones
	Reconnect time.Duration `conf:"reconnect"` // 0 — не переподключать
}

// masterControl — регулятор громкости колонки (пользовательский элемент ALSA; amixer называет его «Master»).
const masterControl = "Master Playback Volume"

const template = `[sink.bluetooth]         # выход «Bluetooth-наушники» (опытный): openmediad bt pair — спарить, bt use — выбрать
enabled = true
device = ""              # адрес наушников (AA:BB:CC:DD:EE:FF); пусто — любые подключённые
aplay = "/usr/bin/aplay"
volume = "speaker"       # speaker — громкость наушников повторяет громкость колонки; headphones — на самих наушниках
reconnect = "30s"        # как часто подключать выбранные наушники, пока их нет; "0s" — не подключать самим`

// platformPID — PID платформы (maind --platformd); 0 — не найдена.
func platformPID() int {
	ps, err := proc.List()
	if err != nil {
		return 0
	}
	if found := proc.Find(ps, func(p proc.Process) bool { return p.HasArg("--platformd") }); len(found) > 0 {
		return found[0].PID
	}
	return 0
}

func init() {
	registry.Register(registry.Component{
		Name: "bluetooth",
		Defaults: func() any {
			return &Config{Toggle: config.Toggle{Enabled: true}, Aplay: "/usr/bin/aplay", Volume: VolumeSpeaker, Reconnect: 30 * time.Second}
		},
		Template: template,
		New: func(env *registry.Env, cfg any) (any, error) {
			c := cfg.(*Config)
			if c.Device != "" && !bluez.ValidAddress(c.Device) {
				return nil, fmt.Errorf("sink.bluetooth.device: %q — нужен адрес вида AA:BB:CC:DD:EE:FF", c.Device)
			}
			if c.Volume != VolumeSpeaker && c.Volume != VolumeHeadphones {
				return nil, fmt.Errorf("sink.bluetooth.volume: %q — допустимо speaker или headphones", c.Volume)
			}
			if c.Reconnect < 0 || c.Reconnect > 0 && c.Reconnect < 5*time.Second {
				return nil, fmt.Errorf("sink.bluetooth.reconnect: %v — 0 или не меньше 5 с", c.Reconnect)
			}
			logf := env.Logf("bluetooth")
			vm, err := LoadVolumeMap(QuasarConfig)
			if err != nil && c.Volume == VolumeSpeaker {
				logf("шкала громкости платформы не прочитана (%v): беру шкалу bergamot", err)
			}
			ctl := &control.Client{}
			buttons := func(b Button) {
				cmd := map[Button]control.Command{ButtonPlayPause: control.Toggle, ButtonStop: control.Pause,
					ButtonNext: control.Next, ButtonPrev: control.Prev}[b]
				if err := ctl.Send(context.Background(), cmd); err != nil {
					logf("кнопка наушников «%s»: платформе не передана: %v", b, err)
					return
				}
				logf("кнопка наушников «%s» → платформе", b)
			}
			return &Sink{
				Device:      strings.ToUpper(c.Device),
				Aplay:       c.Aplay,
				Volume:      c.Volume,
				VolumeMap:   vm,
				Reconnect:   c.Reconnect,
				Logf:        logf,
				OpenMaster:  func() (Master, error) { return alsactl.WatchInteger(masterControl) },
				PlatformPID: platformPID,
				Buttons:     buttons,
			}, nil
		},
	})
}
