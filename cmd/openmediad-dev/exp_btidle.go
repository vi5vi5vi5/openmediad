package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"openmediad/internal/libs/dbus"
)

// btIdleCmd — опыт: адресно (только платформе) сообщить, что транспорт A2DP устройства простаивает, хотя он активен.
// Нужен, чтобы проверить, перестанет ли платформа принимать звук колонки в наушники за телефон (docs/research/HISTORY.md).
func btIdleCmd(args []string) error {
	fs := flag.NewFlagSet("bt-idle", flag.ExitOnError)
	pid := fs.Int("pid", 0, "PID процесса платформы (maind --platformd)")
	state := fs.String("state", "idle", "какое состояние сообщить")
	remove := fs.Bool("remove", false, "вместо состояния сообщить, что транспорт удалён (InterfacesRemoved)")
	add := fs.Bool("add", false, "сообщить, что транспорт появился (InterfacesAdded) — с настоящими свойствами, но State из -state")
	volume := fs.Int("volume", -1, "сообщить громкость транспорта (PropertiesChanged Volume, 0–127)")
	set := fs.Int("set", -1, "по-настоящему поставить громкость транспорта через BlueZ (как кнопки наушников), 0–127")
	fs.Parse(args)
	if fs.NArg() != 1 || *pid == 0 {
		return fmt.Errorf("нужны -pid PID и адрес устройства")
	}
	addr := strings.ToUpper(fs.Arg(0))
	c, err := dbus.DialSystem()
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	names, err := c.Call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "ListNames", "")
	if err != nil {
		return err
	}
	var targets []string
	for _, n := range names[0].([]any) {
		name := n.(string)
		if !strings.HasPrefix(name, ":") {
			continue
		}
		r, err := c.Call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "GetConnectionUnixProcessID", "s", name)
		if err == nil && int(r[0].(uint32)) == *pid {
			targets = append(targets, name)
			fmt.Printf("соединение платформы: %s\n", name)
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("у процесса %d нет соединения с шиной", *pid)
	}
	objs, err := c.Call(ctx, "org.bluez", "/", "org.freedesktop.DBus.ObjectManager", "GetManagedObjects", "")
	if err != nil {
		return err
	}
	devPath := "/org/bluez/hci0/dev_" + strings.ReplaceAll(addr, ":", "_") + "/"
	sent := 0
	for k, v := range objs[0].(map[any]any) {
		path := string(k.(dbus.ObjectPath))
		if !strings.HasPrefix(path, devPath) {
			continue
		}
		ifaces := v.(map[any]any)
		real, ok := ifaces["org.bluez.MediaTransport1"]
		if !ok {
			continue
		}
		if *set >= 0 {
			if _, err := c.Call(ctx, "org.bluez", dbus.ObjectPath(path), "org.freedesktop.DBus.Properties", "Set", "ssv",
				"org.bluez.MediaTransport1", "Volume", dbus.Variant{Sig: "q", Value: uint16(*set)}); err != nil {
				return err
			}
			fmt.Printf("%s: Volume=%d (через BlueZ)\n", path, *set)
			sent++
			continue
		}
		props := map[string]dbus.Variant{"State": {Sig: "s", Value: *state}}
		if *volume >= 0 {
			props = map[string]dbus.Variant{"Volume": {Sig: "q", Value: uint16(*volume)}}
		}
		for _, t := range targets {
			if *add {
				all := map[any]any{}
				for k, x := range real.(map[any]any) {
					all[k] = x
				}
				all["State"] = dbus.Variant{Sig: "s", Value: *state}
				fmt.Printf("свойства: %v\n", all)
				if err := c.Emit(t, "/", "org.freedesktop.DBus.ObjectManager", "InterfacesAdded", "oa{sa{sv}}",
					dbus.ObjectPath(path), map[string]any{"org.bluez.MediaTransport1": all}); err != nil {
					return err
				}
				fmt.Printf("%s: появился (State=%s) → %s\n", path, *state, t)
				continue
			}
			if *remove {
				if err := c.Emit(t, "/", "org.freedesktop.DBus.ObjectManager", "InterfacesRemoved", "oas",
					dbus.ObjectPath(path), []any{"org.bluez.MediaTransport1"}); err != nil {
					return err
				}
				fmt.Printf("%s: удалён → %s\n", path, t)
				continue
			}
			if err := c.Emit(t, dbus.ObjectPath(path), "org.freedesktop.DBus.Properties", "PropertiesChanged", "sa{sv}as",
				"org.bluez.MediaTransport1", props, []any{}); err != nil {
				return err
			}
			fmt.Printf("%s: State=%s → %s\n", path, *state, t)
		}
		sent++
	}
	if sent == 0 {
		fmt.Fprintln(os.Stderr, "транспортов A2DP у устройства нет (наушники не подключены?)")
	}
	time.Sleep(200 * time.Millisecond) // дать сигналу уйти до закрытия соединения
	return nil
}
