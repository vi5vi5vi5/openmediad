package btout

import (
	"context"
	"fmt"
	"strings"
	"time"

	"openmediad/internal/core/cli/command"
	"openmediad/internal/core/config"
	"openmediad/internal/core/platform/proc"
	"openmediad/internal/core/quasar/mediad"
	"openmediad/internal/core/registry"
	"openmediad/internal/libs/aliced-client"
	"openmediad/internal/libs/alsactl"
	"openmediad/internal/libs/bluez"
	_ "openmediad/internal/libs/bt-command" // команда `bt` приходит вместе с компонентом
)

// Config — секция [bt-out].
type Config struct {
	config.Toggle
	Device    string        `conf:"device"`    // адрес наушников; пусто — любые подключённые
	Aplay     string        `conf:"aplay"`     // системный aplay
	Volume    string        `conf:"volume"`    // speaker | headphones
	Reconnect time.Duration `conf:"reconnect"` // 0 — не переподключать
	Hold      bool          `conf:"hold_speaker"`
}

// masterControl — регулятор громкости колонки (пользовательский элемент ALSA; amixer называет его «Master»).
const masterControl = "Master Playback Volume"

const template = `[bt-out]                 # выход «Bluetooth-наушники» (опытный): openmediad bt pair — спарить, bt use — выбрать
enabled = true
device = ""              # адрес наушников (AA:BB:CC:DD:EE:FF); пусто — любые подключённые
aplay = "/usr/bin/aplay"
volume = "speaker"       # speaker — кнопки, голос и экран колонки управляют громкостью наушников; headphones — только сами наушники
hold_speaker = true      # пока подключены наушники, громкость динамиков (голос Алисы) не меняется вместе с ними
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
	command.RegisterTestOutput("bt-out", openHeadphones)
	registry.Register(registry.Component{
		Name: "bt-out",
		Defaults: func() any {
			return &Config{Toggle: config.Toggle{Enabled: true}, Aplay: "/usr/bin/aplay", Volume: VolumeSpeaker, Hold: true, Reconnect: 30 * time.Second}
		},
		Template: template,
		New: func(env *registry.Env, cfg any) (any, error) {
			c := cfg.(*Config)
			if c.Device != "" && !bluez.ValidAddress(c.Device) {
				return nil, fmt.Errorf("bt-out.device: %q — нужен адрес вида AA:BB:CC:DD:EE:FF", c.Device)
			}
			if c.Volume != VolumeSpeaker && c.Volume != VolumeHeadphones {
				return nil, fmt.Errorf("bt-out.volume: %q — допустимо speaker или headphones", c.Volume)
			}
			if c.Reconnect < 0 || c.Reconnect > 0 && c.Reconnect < 5*time.Second {
				return nil, fmt.Errorf("bt-out.reconnect: %v — 0 или не меньше 5 с", c.Reconnect)
			}
			logf := env.Logf("bt-out")
			vm, err := LoadVolumeMap(QuasarConfig)
			if err != nil && c.Volume == VolumeSpeaker {
				logf("шкала громкости платформы не прочитана (%v): беру шкалу bergamot", err)
			}
			ctl := &alicedclient.Client{}
			mach, _ := registry.Use[*mediad.Machine](env, "machine")
			buttons := func(b Button) {
				var err error
				switch {
				case (b == ButtonPlayPause || b == ButtonStop) && mach != nil && mach.Playing():
					// пауза — как голосом: локальная команда ставит паузу без анимации на экране, а директива облака — с ней
					err = ctl.Ask(context.Background(), "поставь на паузу")
				default:
					cmd := map[Button]alicedclient.Command{ButtonPlayPause: alicedclient.Toggle, ButtonStop: alicedclient.Pause,
						ButtonNext: alicedclient.Next, ButtonPrev: alicedclient.Prev}[b]
					err = ctl.Send(context.Background(), cmd)
				}
				if err != nil {
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
				HoldSpeaker: c.Hold,
				PlatformLevel: func(level int) error { // как голосом: «громкость N» (0–10), уровень платформы — 2N
					n := level / 2
					if n == 0 && level > 0 {
						n = 1
					}
					return ctl.Ask(context.Background(), fmt.Sprintf("громкость %d", n))
				},
				Logf:        logf,
				OpenMaster:  func() (Master, error) { return alsactl.WatchInteger(masterControl) },
				PlatformPID: platformPID,
				Buttons:     buttons,
			}, nil
		},
	})
}
