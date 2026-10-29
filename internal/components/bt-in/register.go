package btin

import (
	"fmt"
	"math"
	"os/exec"
	"strings"

	"openmediad/internal/core/config"
	"openmediad/internal/core/engine"
	"openmediad/internal/core/platform/proc"
	"openmediad/internal/core/registry"
	"openmediad/internal/libs/bluez"
	_ "openmediad/internal/libs/bt-command" // команда `bt` приходит вместе с компонентом
)

// Config — секция [bt-in].
type Config struct {
	config.Toggle
	Device  string  `conf:"device"`  // адрес телефона; пусто — любой
	Arecord string  `conf:"arecord"` // системный arecord
	GainDB  float64 `conf:"gain_db"` // громкость относительно звука телефона, дБ
}

const template = `[bt-in]                  # звук с телефона по Bluetooth через openmediad (опытный): приглушение, спектр, маршрут music
enabled = true
device = ""              # адрес телефона (AA:BB:CC:DD:EE:FF); пусто — любой
arecord = "/usr/bin/arecord"
gain_db = -6             # громкость относительно звука телефона; штатный bluealsa-aplay играет на −6 дБ`

// aplayInit — скрипт запуска штатного bluealsa-aplay.
const aplayInit = "/etc/init.d/S53bluealsa-aplay"

// bluealsaAplay — штатная служба bluealsa-aplay (скрипт init.d; rootfs не меняется).
type bluealsaAplay struct{}

func (bluealsaAplay) Running() bool {
	ps, err := proc.List()
	if err != nil {
		return false
	}
	return len(proc.Find(ps, func(p proc.Process) bool { return p.Exe() == "bluealsa-aplay" })) > 0
}

func (bluealsaAplay) Start() error { return run(aplayInit, "start") }
func (bluealsaAplay) Stop() error  { return run(aplayInit, "stop") }

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func init() {
	registry.Register(registry.Component{
		Name: "bt-in",
		Defaults: func() any {
			return &Config{Toggle: config.Toggle{Enabled: true}, Arecord: "/usr/bin/arecord", GainDB: -6}
		},
		Template: template,
		New: func(env *registry.Env, cfg any) (any, error) {
			c := cfg.(*Config)
			if c.Device != "" && !bluez.ValidAddress(c.Device) {
				return nil, fmt.Errorf("bt-in.device: %q — нужен адрес вида AA:BB:CC:DD:EE:FF", c.Device)
			}
			if c.GainDB > 12 || c.GainDB < -40 {
				return nil, fmt.Errorf("bt-in.gain_db: %v — от −40 до 12", c.GainDB)
			}
			eng, ok := registry.Use[*engine.Engine](env, "engine")
			if !ok {
				return nil, fmt.Errorf("btin: нет движка")
			}
			return &Source{
				Device:  strings.ToUpper(c.Device),
				Gain:    math.Pow(10, c.GainDB/20),
				Arecord: c.Arecord,
				Engine:  eng,
				Logf:    env.Logf("bt-in"),
				Aplay:   bluealsaAplay{},
			}, nil
		},
	})
}
