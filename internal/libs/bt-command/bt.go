// Package btcommand — команда терминала `openmediad bt`: поиск, спаривание, подключение и выбор Bluetooth-устройств напрямую
// через системную шину (BlueZ и BlueALSA), без участия демона. Команду приносит не ядро, а этот пакет: он подключается компонентами
// bt-out и bt-in, поэтому в сборке без них нет ни команды, ни пакетов libs/bluez и libs/dbus.
package btcommand

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"openmediad/internal/core/cli/command"
	"openmediad/internal/libs/bluez"
)

func init() { command.Register("Bluetooth", btCmd) }

var btCmd = command.Command{
	Name:    "bt",
	Summary: "Bluetooth-наушники: поиск, спаривание, подключение, выбор для вывода",
	Usage:   "list | scan [-sec 12] | pair АДРЕС | connect АДРЕС | disconnect АДРЕС | forget АДРЕС | use АДРЕС|any",
	Help: `Bluetooth колонки через системную шину (BlueZ и BlueALSA) — то же, чем пользуется платформа.

  list          устройства, их роль (наушники принимают звук, телефон отдаёт) и звуковые потоки BlueALSA
  scan          поиск устройств вокруг (наушники должны быть в режиме спаривания), по умолчанию 12 с
  pair АДРЕС    спарить, доверять и подключить; спаривание включается только на это время
  connect, disconnect АДРЕС   подключить или отключить уже спаренное устройство
  forget АДРЕС  забыть устройство (удалить спаривание)
  use АДРЕС     выводить звук в эти наушники (bt-out.device); any — в любые подключённые

Куда идёт звук каждого класса, задаёт [route] в настройках, например:
  openmediad config set route.music '["bt-out", "speaker"]'   музыка в наушники, без них — в динамики
Видимостью колонки для телефонов по-прежнему управляет Алиса («включи/выключи блютуз»).`,
	Run: func(c *command.Ctx, args []string) error {
		sub := "list"
		if len(args) > 0 {
			sub, args = args[0], args[1:]
		}
		switch sub {
		case "list", "ls":
			if len(args) > 0 {
				return command.UsageError{Msg: "у list нет аргументов"}
			}
			return withBT(func(ctx context.Context, b *bluez.Client) error { return btList(c, ctx, b) })
		case "scan":
			fs := command.Flags(c, "bt scan")
			sec := fs.Int("sec", 12, "сколько секунд искать (1–60)")
			if err := command.Parse(fs, args, 0); err != nil {
				return err
			}
			if *sec < 1 || *sec > 60 {
				return command.UsageError{Msg: "-sec от 1 до 60"}
			}
			return withBT(func(ctx context.Context, b *bluez.Client) error {
				return btScan(c, ctx, b, time.Duration(*sec)*time.Second)
			})
		case "pair", "connect", "disconnect", "forget":
			if len(args) != 1 || !bluez.ValidAddress(args[0]) {
				return command.UsageError{Msg: sub + ": нужен адрес вида AA:BB:CC:DD:EE:FF (его показывают bt list и bt scan)"}
			}
			addr := strings.ToUpper(args[0])
			return withBT(func(ctx context.Context, b *bluez.Client) error { return btDevice(c, ctx, b, sub, addr) })
		case "use":
			if len(args) != 1 || (args[0] != "any" && !bluez.ValidAddress(args[0])) {
				return command.UsageError{Msg: "use: нужен адрес наушников или any"}
			}
			v := ""
			if args[0] != "any" {
				v = strings.ToUpper(args[0])
			}
			return command.SetConfig(c, "bt-out.device", `"`+v+`"`)
		case "-h", "--help", "help":
			command.PrintCommandHelp(c.Stdout, command.Find("bt"))
			return nil
		}
		return command.UsageError{Msg: fmt.Sprintf("неизвестное действие %q", sub)}
	},
}

// withBT — подключиться к шине на время команды. Не колонка (нет системной шины) — command.ErrNotDevice.
func withBT(fn func(ctx context.Context, b *bluez.Client) error) error {
	b, err := bluez.Open()
	if err != nil {
		return fmt.Errorf("%w (%v)", command.ErrNotDevice, err)
	}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return fn(ctx, b)
}

func btList(c *command.Ctx, ctx context.Context, b *bluez.Client) error {
	s, err := b.State(ctx)
	if err != nil {
		return err
	}
	if a, ok := s.Adapter(); ok {
		c.Printf("адаптер %s (%s): %s, виден телефонам: %s, спаривание: %s\n", a.Name, a.Address, pick(a.Powered, "включён", "выключен"),
			yesNo(a.Discoverable), yesNo(a.Pairable))
	} else {
		c.Printf("Bluetooth-адаптера нет\n")
	}
	if len(s.Devices) == 0 {
		c.Printf("известных устройств нет\n")
	} else {
		c.Printf("устройства:\n")
		for _, d := range s.Devices {
			c.Printf("  %s  %-22s %-32s %s\n", d.Address, trimTo(d.Label(), 22), role(d), deviceState(d))
		}
	}
	if len(s.PCMs) > 0 {
		c.Printf("звуковые потоки BlueALSA:\n")
		for _, p := range s.PCMs {
			dir := "звук с устройства"
			if p.Playback() {
				dir = "звук в устройство"
			}
			soft := "громкость на устройстве (AVRCP)"
			if p.SoftVolume {
				soft = "громкость в BlueALSA"
			}
			c.Printf("  %s  %-11s %s, %s %d Гц %d кан., идёт: %s, громкость %d/127, %s\n", p.Address, p.Transport, dir,
				p.Codec, p.Sampling, p.Channels, yesNo(p.Running), p.Volume>>8&0x7f, soft)
		}
	}
	return nil
}

func btScan(c *command.Ctx, ctx context.Context, b *bluez.Client, d time.Duration) error {
	s, err := b.State(ctx)
	if err != nil {
		return err
	}
	a, ok := s.Adapter()
	if !ok {
		return errors.New("нет Bluetooth-адаптера")
	}
	before := map[string]bool{}
	for _, d := range s.Devices {
		before[d.Address] = true
	}
	if err := b.StartDiscovery(ctx, a.Path); err != nil {
		return fmt.Errorf("поиск: %w", err)
	}
	c.Printf("ищу %s (наушники должны быть в режиме спаривания)…\n", d)
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
	stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b.StopDiscovery(stop, a.Path)
	s, err = b.State(stop)
	if err != nil {
		return err
	}
	found := 0
	for _, d := range s.Devices {
		if d.RSSI == 0 && before[d.Address] {
			continue // известное устройство, при поиске не отозвалось
		}
		found++
		mark := ""
		if !before[d.Address] {
			mark = " (новое)"
		}
		c.Printf("  %s  %-22s %-32s сигнал %d дБм%s\n", d.Address, trimTo(d.Label(), 22), role(d), d.RSSI, mark)
	}
	if found == 0 {
		c.Printf("ничего не найдено\n")
	} else {
		c.Printf("спарить: openmediad bt pair АДРЕС\n")
	}
	return nil
}

func btDevice(c *command.Ctx, ctx context.Context, b *bluez.Client, action, addr string) error {
	if action == "pair" {
		c.Printf("спариваю %s (агент колонки подтвердит сам)…\n", addr)
		if err := b.PairAndTrust(ctx, addr); err != nil {
			return err
		}
		c.Printf("готово: спарено, доверено, подключено. Выводить в эти наушники: openmediad bt use %s\n", addr)
		return nil
	}
	s, err := b.State(ctx)
	if err != nil {
		return err
	}
	d, ok := s.Device(addr)
	if !ok {
		return fmt.Errorf("устройство %s не известно BlueZ (bt list)", addr)
	}
	switch action {
	case "connect":
		err = b.Connect(ctx, d.Path)
	case "disconnect":
		err = b.Disconnect(ctx, d.Path)
	case "forget":
		err = b.Remove(ctx, d.Adapter, d.Path)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", action, addr, err)
	}
	c.Printf("%s %s: готово\n", action, d.Label())
	return nil
}

func role(d bluez.Device) string {
	switch {
	case d.AudioSink() && d.AudioSource():
		return "приёмник и источник звука"
	case d.AudioSink():
		return "наушники/колонка (приёмник)"
	case d.AudioSource():
		return "телефон (источник звука)"
	}
	return "без звуковых профилей"
}

func deviceState(d bluez.Device) string {
	var s []string
	if d.Paired {
		s = append(s, "спарено")
	}
	if d.Trusted {
		s = append(s, "доверено")
	}
	s = append(s, pick(d.Connected, "подключено", "не подключено"))
	return strings.Join(s, ", ")
}

func pick(b bool, on, off string) string {
	if b {
		return on
	}
	return off
}

func yesNo(b bool) string { return pick(b, "да", "нет") }

func trimTo(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
