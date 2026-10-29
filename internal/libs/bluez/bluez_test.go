package bluez

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"openmediad/internal/libs/dbus"
)

func v(sig string, x any) dbus.Variant { return dbus.Variant{Sig: sig, Value: x} }

// Ответы GetManagedObjects в том виде, в каком их разбирает клиент шины (как на колонке 28.10.2026: адаптер, телефон,
// и подключённые наушники с PCM BlueALSA в обе стороны).
func bluezObjects() map[any]any {
	return map[any]any{
		dbus.ObjectPath("/org/bluez/hci0"): map[any]any{
			"org.bluez.Adapter1": map[any]any{"Address": v("s", "AC:BA:C0:D4:C6:89"), "Alias": v("s", "Станция Мини 3"),
				"Powered": v("b", true), "Discoverable": v("b", false), "Pairable": v("b", false), "Discovering": v("b", false)},
		},
		dbus.ObjectPath("/org/bluez/hci0/dev_CC_FF_90_DD_63_86"): map[any]any{
			"org.bluez.Device1": map[any]any{"Address": v("s", "CC:FF:90:DD:63:86"), "Name": v("s", "hytler126"), "Alias": v("s", "hytler126"),
				"Adapter": v("o", dbus.ObjectPath("/org/bluez/hci0")), "Paired": v("b", true), "Connected": v("b", false),
				"UUIDs": v("as", []any{"0000110a-0000-1000-8000-00805f9b34fb", "0000110e-0000-1000-8000-00805f9b34fb"})},
		},
		dbus.ObjectPath("/org/bluez/hci0/dev_C0_DA_5E_53_03_B1"): map[any]any{
			"org.bluez.Device1": map[any]any{"Address": v("s", "C0:DA:5E:53:03:B1"), "Name": v("s", "HUAWEI FreeBuds Pro 5"),
				"Icon": v("s", "audio-headphones"), "Class": v("u", uint32(0x244404)), "Adapter": v("o", dbus.ObjectPath("/org/bluez/hci0")),
				"Paired": v("b", true), "Trusted": v("b", true), "Connected": v("b", true), "RSSI": v("n", int16(-58)),
				"UUIDs": v("as", []any{"0000110b-0000-1000-8000-00805f9b34fb", "0000111e-0000-1000-8000-00805f9b34fb"})},
			"org.bluez.MediaControl1": map[any]any{},
		},
	}
}

func bluealsaObjects() map[any]any {
	pcm := func(dev, transport, mode string, vol uint16) map[any]any {
		return map[any]any{"org.bluealsa.PCM1": map[any]any{"Device": v("o", dbus.ObjectPath("/org/bluez/hci0/"+dev)),
			"Transport": v("s", transport), "Mode": v("s", mode), "Running": v("b", false), "Format": v("q", uint16(33296)),
			"Channels": v("y", byte(2)), "Sampling": v("u", uint32(48000)), "Codec": v("s", "SBC"), "Delay": v("q", uint16(150)),
			"SoftVolume": v("b", false), "Volume": v("q", vol)}}
	}
	return map[any]any{
		dbus.ObjectPath("/org/bluealsa/hci0/dev_C0_DA_5E_53_03_B1/a2dpsrc/sink"):   pcm("dev_C0_DA_5E_53_03_B1", "A2DP-source", "sink", 0x7f7f),
		dbus.ObjectPath("/org/bluealsa/hci0/dev_C0_DA_5E_53_03_B1/hfpag/sink"):     pcm("dev_C0_DA_5E_53_03_B1", "HFP-AG", "sink", 0x0f0f),
		dbus.ObjectPath("/org/bluealsa/hci0/dev_CC_FF_90_DD_63_86/a2dpsnk/source"): pcm("dev_CC_FF_90_DD_63_86", "A2DP-sink", "source", 3341),
	}
}

// fakeBus — шина на ответах из карты: метод → ответ или ошибка; записывает вызовы.
type fakeBus struct {
	calls []string
	reply map[string][]any
	fail  map[string]error
}

func (f *fakeBus) Call(_ context.Context, dest string, path dbus.ObjectPath, iface, member, sig string, args ...any) ([]any, error) {
	key := dest + " " + string(path) + " " + iface + "." + member
	failKey := dest + " " + member
	if sig != "" {
		key += fmt.Sprintf(" %s %v", sig, args)
	}
	f.calls = append(f.calls, key)
	if err := f.fail[failKey]; err != nil {
		return nil, err
	}
	return f.reply[failKey], nil
}

func newFake() *fakeBus {
	return &fakeBus{
		reply: map[string][]any{
			"org.bluez GetManagedObjects":    {bluezObjects()},
			"org.bluealsa GetManagedObjects": {bluealsaObjects()},
		},
		fail: map[string]error{},
	}
}

func TestStateParsesDevicesAndPCMs(t *testing.T) {
	b := &Client{Bus: newFake()}
	s, err := b.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a, ok := s.Adapter()
	if !ok || a.Address != "AC:BA:C0:D4:C6:89" || !a.Powered || a.Pairable {
		t.Fatalf("адаптер %+v", a)
	}
	if len(s.Devices) != 2 {
		t.Fatalf("устройств %d", len(s.Devices))
	}
	phone, _ := s.Device("cc:ff:90:dd:63:86")
	if !phone.AudioSource() || phone.AudioSink() || phone.Connected || phone.Adapter != "/org/bluez/hci0" {
		t.Fatalf("телефон %+v", phone)
	}
	hp, _ := s.Device("C0:DA:5E:53:03:B1")
	if !hp.AudioSink() || hp.AudioSource() || !hp.Connected || !hp.Trusted || hp.RSSI != -58 || hp.Class != 0x244404 || hp.Label() != "HUAWEI FreeBuds Pro 5" {
		t.Fatalf("наушники %+v", hp)
	}
	p, ok := s.PlaybackPCM("")
	if !ok || p.Address != "C0:DA:5E:53:03:B1" || p.Transport != "A2DP-source" || p.Sampling != 48000 || p.Channels != 2 || p.Volume != 0x7f7f {
		t.Fatalf("PCM наушников %+v", p)
	}
	if _, ok := s.PlaybackPCM("CC:FF:90:DD:63:86"); ok {
		t.Fatal("у телефона PCM только «с устройства»: в него не играют")
	}
	if p, ok := s.PlaybackPCM("c0:da:5e:53:03:b1"); !ok || p.Transport == "HFP-AG" {
		t.Fatalf("для вывода музыки — только A2DP, не гарнитура: %+v", p)
	}
}

func TestStateWithoutBlueALSA(t *testing.T) {
	f := newFake()
	f.fail["org.bluealsa GetManagedObjects"] = &dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}
	s, err := (&Client{Bus: f}).State(context.Background())
	if err != nil || len(s.PCMs) != 0 || len(s.Devices) != 2 {
		t.Fatalf("без BlueALSA: %v, %+v", err, s)
	}
	f.fail["org.bluez GetManagedObjects"] = errors.New("нет")
	if _, err := (&Client{Bus: f}).State(context.Background()); err == nil {
		t.Fatal("без BlueZ — ошибка")
	}
}

// Спаривание включает Pairable только на время и возвращает как было; телефон по ходу не трогается.
func TestPairAndTrustRestoresPairable(t *testing.T) {
	f := newFake()
	b := &Client{Bus: f}
	// наушники ещё не спарены
	objs := bluezObjects()
	dev := objs[dbus.ObjectPath("/org/bluez/hci0/dev_C0_DA_5E_53_03_B1")].(map[any]any)["org.bluez.Device1"].(map[any]any)
	dev["Paired"], dev["Connected"] = v("b", false), v("b", false)
	f.reply["org.bluez GetManagedObjects"] = []any{objs}
	if err := b.PairAndTrust(context.Background(), "c0:da:5e:53:03:b1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"org.bluez / org.freedesktop.DBus.ObjectManager.GetManagedObjects",
		"org.bluealsa /org/bluealsa org.freedesktop.DBus.ObjectManager.GetManagedObjects",
		"org.bluez /org/bluez/hci0 org.freedesktop.DBus.Properties.Set ssv [org.bluez.Adapter1 Pairable {b true}]",
		"org.bluez /org/bluez/hci0/dev_C0_DA_5E_53_03_B1 org.bluez.Device1.Pair",
		"org.bluez /org/bluez/hci0/dev_C0_DA_5E_53_03_B1 org.freedesktop.DBus.Properties.Set ssv [org.bluez.Device1 Trusted {b true}]",
		"org.bluez /org/bluez/hci0/dev_C0_DA_5E_53_03_B1 org.bluez.Device1.Connect",
		"org.bluez /org/bluez/hci0 org.freedesktop.DBus.Properties.Set ssv [org.bluez.Adapter1 Pairable {b false}]",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("вызовы:\n%s\nждали:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestPairFailureStillRestoresPairable(t *testing.T) {
	f := newFake()
	objs := bluezObjects()
	objs[dbus.ObjectPath("/org/bluez/hci0/dev_C0_DA_5E_53_03_B1")].(map[any]any)["org.bluez.Device1"].(map[any]any)["Paired"] = v("b", false)
	f.reply["org.bluez GetManagedObjects"] = []any{objs}
	f.fail["org.bluez Pair"] = &dbus.Error{Name: "org.bluez.Error.AuthenticationFailed"}
	if err := (&Client{Bus: f}).PairAndTrust(context.Background(), "C0:DA:5E:53:03:B1"); err == nil {
		t.Fatal("ошибка спаривания должна вернуться")
	}
	if last := f.calls[len(f.calls)-1]; !strings.HasSuffix(last, "Pairable {b false}]") {
		t.Fatalf("Pairable не возвращён: %s", last)
	}
	if err := (&Client{Bus: newFake()}).PairAndTrust(context.Background(), "11:22:33:44:55:66"); err == nil || !strings.Contains(err.Error(), "не найдено") {
		t.Fatalf("неизвестное устройство: %v", err)
	}
}

func TestPCMVolume(t *testing.T) {
	f := newFake()
	b := &Client{Bus: f}
	b.SetPCMVolume(context.Background(), "/org/bluealsa/x", 200)
	b.SetPCMSoftVolume(context.Background(), "/org/bluealsa/x", true)
	want := "org.bluealsa /org/bluealsa/x org.freedesktop.DBus.Properties.Set ssv [org.bluealsa.PCM1 Volume {q 32639}]" // 0x7f7f
	if f.calls[0] != want || !strings.HasSuffix(f.calls[1], "SoftVolume {b true}]") {
		t.Fatalf("вызовы: %v", f.calls)
	}
}

func TestAddressHelpers(t *testing.T) {
	if a := AddressOf("/org/bluealsa/hci0/dev_C0_DA_5E_53_03_B1/a2dpsrc/sink"); a != "C0:DA:5E:53:03:B1" {
		t.Fatalf("AddressOf: %q", a)
	}
	if AddressOf("/org/bluez/hci0") != "" {
		t.Fatal("у адаптера адреса устройства нет")
	}
	for a, ok := range map[string]bool{"C0:DA:5E:53:03:B1": true, "c0:da:5e:53:03:b1": true, "C0-DA-5E-53-03-B1": false, "C0:DA:5E:53:03": false, "G0:DA:5E:53:03:B1": false} {
		if ValidAddress(a) != ok {
			t.Errorf("ValidAddress(%q) != %v", a, ok)
		}
	}
}

// emitBus — fakeBus, который ещё и отправляет сигналы (как настоящее соединение).
type emitBus struct {
	fakeBus
	sent []string
}

func (e *emitBus) Emit(dest string, path dbus.ObjectPath, iface, member, sig string, args ...any) error {
	e.sent = append(e.sent, fmt.Sprintf("%s %s %s.%s %s %v", dest, path, iface, member, sig, args))
	return nil
}

// Платформе (и только ей) — адресно: «транспорт удалён» от ObjectManager и «простаивает» от самого транспорта.
func TestForgetAndHideTransport(t *testing.T) {
	b := &emitBus{}
	c := &Client{Bus: b}
	tr := dbus.ObjectPath("/org/bluez/hci0/dev_A0/sep1/fd8")
	if err := c.ForgetTransport([]string{":1.5", ":1.6"}, tr); err != nil {
		t.Fatal(err)
	}
	if err := c.HideTransport([]string{":1.5"}, tr); err != nil {
		t.Fatal(err)
	}
	want := []string{
		":1.5 / org.freedesktop.DBus.ObjectManager.InterfacesRemoved oas [/org/bluez/hci0/dev_A0/sep1/fd8 [org.bluez.MediaTransport1]]",
		":1.6 / org.freedesktop.DBus.ObjectManager.InterfacesRemoved oas [/org/bluez/hci0/dev_A0/sep1/fd8 [org.bluez.MediaTransport1]]",
		":1.5 /org/bluez/hci0/dev_A0/sep1/fd8 org.freedesktop.DBus.Properties.PropertiesChanged sa{sv}as [org.bluez.MediaTransport1 map[State:{s idle}] []]",
	}
	if strings.Join(b.sent, "\n") != strings.Join(want, "\n") {
		t.Fatalf("отправлено:\n%s", strings.Join(b.sent, "\n"))
	}
	if err := (&Client{Bus: newFake()}).ForgetTransport([]string{":1.5"}, tr); err == nil {
		t.Fatal("шина без сигналов — ошибка")
	}
}

// Платформе транспорт возвращается с настоящими свойствами, но простаивающим.
func TestShowTransport(t *testing.T) {
	b := &emitBus{fakeBus: fakeBus{reply: map[string][]any{"org.bluez GetAll": {map[any]any{
		"UUID":   dbus.Variant{Sig: "s", Value: UUIDAudioSource},
		"State":  dbus.Variant{Sig: "s", Value: "active"},
		"Volume": dbus.Variant{Sig: "q", Value: uint16(57)},
	}}}, fail: map[string]error{}}}
	c := &Client{Bus: b}
	tr := dbus.ObjectPath("/org/bluez/hci0/dev_A0/sep1/fd9")
	if err := c.ShowTransport(context.Background(), []string{":1.5"}, tr); err != nil {
		t.Fatal(err)
	}
	want := ":1.5 / org.freedesktop.DBus.ObjectManager.InterfacesAdded oa{sa{sv}} [/org/bluez/hci0/dev_A0/sep1/fd9 " +
		"map[org.bluez.MediaTransport1:map[State:{s idle} UUID:{s 0000110a-0000-1000-8000-00805f9b34fb} Volume:{q 57}]]]"
	if len(b.sent) != 1 || !strings.EqualFold(b.sent[0], want) {
		t.Fatalf("отправлено:\n%v\nждали:\n%s", b.sent, want)
	}
	if b.calls[0] != "org.bluez "+string(tr)+" org.freedesktop.DBus.Properties.GetAll s [org.bluez.MediaTransport1]" {
		t.Fatalf("вызов %q", b.calls[0])
	}
}
