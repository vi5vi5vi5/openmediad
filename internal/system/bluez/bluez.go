// Package bluez — Bluetooth колонки через системную шину: BlueZ (org.bluez: адаптер, устройства, спаривание, подключение)
// и BlueALSA (org.bluealsa: звуковые PCM подключённых устройств). Стек поднимают скрипты S50–S54 до Quasar, платформа
// пользуется им через ту же шину и знает Bluetooth только как приёмник звука с телефона (docs/BUS-PROTOCOL.md, §2.7–2.7а).
// Этот пакет нужен для обратного направления — колонка играет в наушники (выход sink/bluetooth, команды openmediad bt).
//
// Видимостью колонки (Discoverable) владеет платформа («включи/выключи блютуз»); пакет её не трогает, а возможность
// спаривания (Pairable) включает только на время спаривания и возвращает как было.
package bluez

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"openmediad/internal/system/dbus"
)

// Службы Bluetooth (UUID профилей).
const (
	UUIDAudioSource = "0000110a-0000-1000-8000-00805f9b34fb" // устройство отдаёт звук (телефон)
	UUIDAudioSink   = "0000110b-0000-1000-8000-00805f9b34fb" // устройство принимает звук (наушники, колонка)
)

const (
	bluezService    = "org.bluez"
	bluealsaService = "org.bluealsa"
	ifaceAdapter    = "org.bluez.Adapter1"
	ifaceDevice     = "org.bluez.Device1"
	ifacePCM        = "org.bluealsa.PCM1"
	ifaceTransport  = "org.bluez.MediaTransport1"
	ifaceProps      = "org.freedesktop.DBus.Properties"
	ifaceObjMgr     = "org.freedesktop.DBus.ObjectManager"
)

// Adapter — Bluetooth-адаптер колонки (hci0).
type Adapter struct {
	Path         dbus.ObjectPath
	Address      string
	Name         string
	Powered      bool
	Discoverable bool
	Pairable     bool
	Discovering  bool
}

// Device — известное BlueZ устройство.
type Device struct {
	Path      dbus.ObjectPath
	Adapter   dbus.ObjectPath
	Address   string
	Name      string
	Alias     string
	Icon      string // подсказка BlueZ: audio-headphones, audio-headset, audio-card, phone, computer…
	Class     uint32
	Paired    bool
	Bonded    bool
	Trusted   bool
	Connected bool
	UUIDs     []string
	RSSI      int16 // уровень сигнала, пока устройство видно при поиске (0 — неизвестно)
}

// AudioSink — устройство умеет принимать звук (наушники, колонка): в него можно играть.
func (d Device) AudioSink() bool { return hasUUID(d.UUIDs, UUIDAudioSink) }

// AudioSource — устройство умеет отдавать звук (телефон, компьютер): его звук играет колонка (bluealsa-aplay).
func (d Device) AudioSource() bool { return hasUUID(d.UUIDs, UUIDAudioSource) }

// Label — как называть устройство человеку.
func (d Device) Label() string {
	switch {
	case d.Alias != "":
		return d.Alias
	case d.Name != "":
		return d.Name
	}
	return d.Address
}

// PCM — звуковой поток BlueALSA к подключённому устройству.
type PCM struct {
	Path       dbus.ObjectPath
	Device     dbus.ObjectPath
	Address    string
	Transport  string // A2DP-source (колонка отдаёт звук), A2DP-sink (принимает), HFP-AG и т. п.
	Mode       string // sink — в PCM пишут (звук уходит в устройство), source — из PCM читают
	Running    bool
	Format     uint16
	Channels   byte
	Sampling   uint32
	Codec      string
	Delay      uint16 // задержка в десятых долях миллисекунды (как в BlueALSA)
	SoftVolume bool
	Volume     uint16 // старший байт — левый канал, младший — правый; 0–127, бит 0x80 — без звука
}

// Playback — в PCM пишут: звук колонки уходит в устройство (наушники).
func (p PCM) Playback() bool { return p.Mode == "sink" }

// A2DP — профиль музыки (стерео), а не гарнитуры.
func (p PCM) A2DP() bool { return strings.HasPrefix(p.Transport, "A2DP") }

// Transport — транспорт A2DP BlueZ (канал звука с одним устройством).
type Transport struct {
	Path    dbus.ObjectPath
	Device  dbus.ObjectPath
	Address string
	UUID    string // своя роль колонки: Audio Source — колонка играет в устройство (наушники), Audio Sink — принимает (телефон)
	State   string // idle, pending, active
}

// ToHeadphones — колонка отдаёт звук в устройство (транспорт её источника A2DP).
func (t Transport) ToHeadphones() bool { return strings.EqualFold(t.UUID, UUIDAudioSource) }

// Active — звук по транспорту идёт (или вот-вот пойдёт).
func (t Transport) Active() bool { return t.State == "active" || t.State == "pending" }

// State — снимок Bluetooth колонки.
type State struct {
	Adapters   []Adapter
	Devices    []Device
	PCMs       []PCM
	Transports []Transport
}

// Device ищет устройство по адресу (регистр не важен).
func (s State) Device(addr string) (Device, bool) {
	for _, d := range s.Devices {
		if strings.EqualFold(d.Address, addr) {
			return d, true
		}
	}
	return Device{}, false
}

// PlaybackPCM — PCM для вывода музыки в устройство addr (пусто — в любое подключённое): A2DP, в который пишут.
func (s State) PlaybackPCM(addr string) (PCM, bool) {
	for _, p := range s.PCMs {
		if p.Playback() && p.A2DP() && (addr == "" || strings.EqualFold(p.Address, addr)) {
			return p, true
		}
	}
	return PCM{}, false
}

// Adapter — первый адаптер (у колонки он один).
func (s State) Adapter() (Adapter, bool) {
	if len(s.Adapters) == 0 {
		return Adapter{}, false
	}
	return s.Adapters[0], true
}

// Caller — вызов метода на шине (dbus.Conn; в тестах — подделка).
type Caller interface {
	Call(ctx context.Context, dest string, path dbus.ObjectPath, iface, member, sig string, args ...any) ([]any, error)
}

// Client — Bluetooth колонки через шину.
type Client struct {
	Bus  Caller
	conn *dbus.Conn // для подписок (nil в тестах)
}

// Open подключается к системной шине.
func Open() (*Client, error) {
	c, err := dbus.DialSystem()
	if err != nil {
		return nil, err
	}
	return &Client{Bus: c, conn: c}, nil
}

// Close закрывает соединение с шиной.
func (c *Client) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// Done закрывается, когда соединение с шиной умерло (шина или BlueZ перезапустились).
func (c *Client) Done() <-chan struct{} {
	if c.conn == nil {
		return nil
	}
	return c.conn.Done()
}

// State читает адаптеры, устройства и PCM. Если BlueALSA не запущена, PCM пустые — это не ошибка.
func (c *Client) State(ctx context.Context) (State, error) {
	var s State
	objs, err := c.managed(ctx, bluezService, "/")
	if err != nil {
		return s, fmt.Errorf("BlueZ: %w", err)
	}
	s.Adapters, s.Devices, s.Transports = parseBluez(objs)
	pcms, err := c.managed(ctx, bluealsaService, "/org/bluealsa")
	var de *dbus.Error
	switch {
	case errors.As(err, &de) && de.Name == "org.freedesktop.DBus.Error.ServiceUnknown":
	case err != nil:
		return s, fmt.Errorf("BlueALSA: %w", err)
	default:
		s.PCMs = parsePCMs(pcms)
	}
	return s, nil
}

func (c *Client) managed(ctx context.Context, svc string, path dbus.ObjectPath) (map[any]any, error) {
	body, err := c.Bus.Call(ctx, svc, path, ifaceObjMgr, "GetManagedObjects", "")
	if err != nil {
		return nil, err
	}
	if len(body) != 1 {
		return nil, errors.New("GetManagedObjects: пустой ответ")
	}
	m, ok := body[0].(map[any]any)
	if !ok {
		return nil, fmt.Errorf("GetManagedObjects: ответ %T", body[0])
	}
	return m, nil
}

// --- действия ---

// Connect подключает устройство (все профили, которые оно поддерживает).
func (c *Client) Connect(ctx context.Context, dev dbus.ObjectPath) error {
	_, err := c.Bus.Call(ctx, bluezService, dev, ifaceDevice, "Connect", "")
	return err
}

// Disconnect отключает устройство.
func (c *Client) Disconnect(ctx context.Context, dev dbus.ObjectPath) error {
	_, err := c.Bus.Call(ctx, bluezService, dev, ifaceDevice, "Disconnect", "")
	return err
}

// Pair спаривает устройство. Подтверждение берёт на себя системный агент колонки (just-works-bluez-agent).
func (c *Client) Pair(ctx context.Context, dev dbus.ObjectPath) error {
	_, err := c.Bus.Call(ctx, bluezService, dev, ifaceDevice, "Pair", "")
	return err
}

// SetTrusted — доверять устройству: BlueZ пускает его без вопросов (переподключение наушников).
func (c *Client) SetTrusted(ctx context.Context, dev dbus.ObjectPath, on bool) error {
	return c.setProp(ctx, bluezService, dev, ifaceDevice, "Trusted", dbus.Variant{Sig: "b", Value: on})
}

// Remove забывает устройство (спаривание удаляется).
func (c *Client) Remove(ctx context.Context, adapter, dev dbus.ObjectPath) error {
	_, err := c.Bus.Call(ctx, bluezService, adapter, ifaceAdapter, "RemoveDevice", "o", dev)
	return err
}

// StartDiscovery и StopDiscovery — поиск устройств вокруг.
func (c *Client) StartDiscovery(ctx context.Context, adapter dbus.ObjectPath) error {
	_, err := c.Bus.Call(ctx, bluezService, adapter, ifaceAdapter, "StartDiscovery", "")
	return err
}

func (c *Client) StopDiscovery(ctx context.Context, adapter dbus.ObjectPath) error {
	_, err := c.Bus.Call(ctx, bluezService, adapter, ifaceAdapter, "StopDiscovery", "")
	return err
}

// SetPairable включает или выключает спаривание (без него BlueZ не сохраняет ключи и при спаривании, начатом колонкой).
func (c *Client) SetPairable(ctx context.Context, adapter dbus.ObjectPath, on bool) error {
	return c.setProp(ctx, bluezService, adapter, ifaceAdapter, "Pairable", dbus.Variant{Sig: "b", Value: on})
}

// SetPCMVolume задаёт громкость PCM BlueALSA (оба канала, 0–127).
func (c *Client) SetPCMVolume(ctx context.Context, pcm dbus.ObjectPath, level byte) error {
	if level > 127 {
		level = 127
	}
	v := uint16(level)<<8 | uint16(level)
	return c.setProp(ctx, bluealsaService, pcm, ifacePCM, "Volume", dbus.Variant{Sig: "q", Value: v})
}

// SetPCMSoftVolume: true — громкость меняет сама BlueALSA (масштабирует звук), false — передаёт устройству (AVRCP).
func (c *Client) SetPCMSoftVolume(ctx context.Context, pcm dbus.ObjectPath, on bool) error {
	return c.setProp(ctx, bluealsaService, pcm, ifacePCM, "SoftVolume", dbus.Variant{Sig: "b", Value: on})
}

func (c *Client) setProp(ctx context.Context, svc string, path dbus.ObjectPath, iface, name string, v dbus.Variant) error {
	_, err := c.Bus.Call(ctx, svc, path, ifaceProps, "Set", "ssv", iface, name, v)
	return err
}

// PairAndTrust — спаривание наушников: на время включает Pairable (если было выключено), спаривает, доверяет и подключает;
// Pairable возвращает как было. Устройство должно быть в режиме спаривания и найдено поиском.
func (c *Client) PairAndTrust(ctx context.Context, addr string) error {
	s, err := c.State(ctx)
	if err != nil {
		return err
	}
	a, ok := s.Adapter()
	if !ok {
		return errors.New("нет Bluetooth-адаптера")
	}
	d, ok := s.Device(addr)
	if !ok {
		return fmt.Errorf("устройство %s не найдено: включите на нём режим спаривания и выполните поиск", addr)
	}
	if !a.Pairable {
		if err := c.SetPairable(ctx, a.Path, true); err != nil {
			return fmt.Errorf("включить спаривание: %w", err)
		}
		defer func() {
			back, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c.SetPairable(back, a.Path, false)
		}()
	}
	if !d.Paired {
		if err := c.Pair(ctx, d.Path); err != nil {
			return fmt.Errorf("спаривание: %w", err)
		}
	}
	if err := c.SetTrusted(ctx, d.Path, true); err != nil {
		return fmt.Errorf("доверие: %w", err)
	}
	if err := c.Connect(ctx, d.Path); err != nil {
		return fmt.Errorf("подключение: %w", err)
	}
	return nil
}

// --- платформа ---

// Emitter — отправка сигнала (dbus.Conn; в тестах — подделка).
type Emitter interface {
	Emit(dest string, path dbus.ObjectPath, iface, member, sig string, args ...any) error
}

// Connections — уникальные имена соединений процесса pid с шиной (у платформы их несколько).
func (c *Client) Connections(ctx context.Context, pid int) ([]string, error) {
	body, err := c.Bus.Call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "ListNames", "")
	if err != nil {
		return nil, err
	}
	names, _ := body[0].([]any)
	var out []string
	for _, n := range names {
		name, _ := n.(string)
		if !strings.HasPrefix(name, ":") {
			continue
		}
		r, err := c.Bus.Call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "GetConnectionUnixProcessID", "s", name)
		if err != nil || len(r) != 1 {
			continue
		}
		if p, ok := r[0].(uint32); ok && int(p) == pid {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// HideTransport адресно — только соединениям dests — сообщает, что транспорт простаивает (State = idle), хотя он
// активен. Нужно из-за платформы: она считает любой активный транспорт A2DP звуком телефона, и звук колонки в наушники
// для неё — «играет Bluetooth-плеер»: она забирает аудиофокус и отменяет музыку (проверено 28.10.2026, BUS-PROTOCOL.md §2.7б).
// Адресный сигнал получает только платформа; BlueALSA и остальные клиенты шины его не видят.
func (c *Client) HideTransport(dests []string, path dbus.ObjectPath) error {
	props := map[string]dbus.Variant{"State": {Sig: "s", Value: "idle"}}
	return c.emitTo(dests, path, ifaceProps, "PropertiesChanged", "sa{sv}as", ifaceTransport, props, []any{})
}

// ForgetTransport адресно сообщает соединениям dests, что транспорта больше нет (InterfacesRemoved от ObjectManager BlueZ).
// Платформа после этого перестаёт следить за ним и его включения не видит вовсе — в отличие от HideTransport, который
// исправляет уже случившееся (проверено 28.10.2026, BUS-PROTOCOL.md §2.7б). Сам транспорт, BlueALSA и остальные клиенты
// шины не затронуты.
func (c *Client) ForgetTransport(dests []string, path dbus.ObjectPath) error {
	return c.emitTo(dests, "/", "org.freedesktop.DBus.ObjectManager", "InterfacesRemoved", "oas", path, []any{ifaceTransport})
}

// ShowTransport — обратное ForgetTransport: адресно сообщает соединениям dests, что транспорт есть (InterfacesAdded), с его
// настоящими свойствами, но State = idle. Платформа снова следит за ним — видит громкость наушников (их кнопки, экран
// колонки), — но считает его простаивающим. Пока канал в наушники открыт, BlueZ состояние не меняет, и она в это верит
// (проверено 28.10.2026, BUS-PROTOCOL.md §2.7б).
func (c *Client) ShowTransport(ctx context.Context, dests []string, path dbus.ObjectPath) error {
	r, err := c.Bus.Call(ctx, bluezService, path, ifaceProps, "GetAll", "s", ifaceTransport)
	if err != nil {
		return err
	}
	if len(r) != 1 {
		return errors.New("bluez: GetAll транспорта: пустой ответ")
	}
	real, ok := r[0].(map[any]any)
	if !ok {
		return fmt.Errorf("bluez: GetAll транспорта: %T", r[0])
	}
	props := make(map[any]any, len(real))
	for k, v := range real {
		props[k] = v
	}
	props["State"] = dbus.Variant{Sig: "s", Value: "idle"}
	return c.emitTo(dests, "/", "org.freedesktop.DBus.ObjectManager", "InterfacesAdded", "oa{sa{sv}}", path,
		map[string]any{ifaceTransport: props})
}

func (c *Client) emitTo(dests []string, path dbus.ObjectPath, iface, member, sig string, args ...any) error {
	e, ok := c.Bus.(Emitter)
	if !ok {
		return errors.New("bluez: шина не умеет отправлять сигналы")
	}
	for _, d := range dests {
		if err := e.Emit(d, path, iface, member, sig, args...); err != nil {
			return err
		}
	}
	return nil
}

// --- наблюдение ---

// Watch присылает в канал отметку (без подробностей), когда у BlueZ или BlueALSA что-то поменялось: устройство
// подключилось или отключилось, появился или исчез PCM. Несколько изменений подряд сливаются в одну отметку. Канал
// закрывается, когда соединение с шиной умерло или ctx отменён.
func (c *Client) Watch(ctx context.Context) (<-chan struct{}, error) {
	if c.conn == nil {
		return nil, errors.New("bluez: нет соединения с шиной")
	}
	var subs []*dbus.Subscription
	for _, m := range []dbus.Match{
		{Sender: bluezService, Interface: ifaceObjMgr},
		{Sender: bluezService, Interface: ifaceProps, Member: "PropertiesChanged"},
		{Sender: bluealsaService, Interface: ifaceObjMgr},
		{Sender: bluealsaService, Interface: ifaceProps, Member: "PropertiesChanged"},
	} {
		s, err := c.conn.Subscribe(ctx, m)
		if err != nil {
			for _, s := range subs {
				s.Close()
			}
			return nil, err
		}
		subs = append(subs, s)
	}
	out := make(chan struct{}, 1)
	tick := func() {
		select {
		case out <- struct{}{}:
		default:
		}
	}
	merged := make(chan struct{}, 1)
	for _, s := range subs {
		go func(s *dbus.Subscription) {
			for range s.C {
				select {
				case merged <- struct{}{}:
				default:
				}
			}
		}(s)
	}
	go func() {
		defer close(out)
		defer func() {
			for _, s := range subs {
				s.Close()
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-c.conn.Done():
				return
			case <-merged:
				tick()
			}
		}
	}()
	return out, nil
}

// --- разбор ответов ---

func parseBluez(objs map[any]any) (adapters []Adapter, devices []Device, transports []Transport) {
	for k, v := range objs {
		path, _ := k.(dbus.ObjectPath)
		ifaces, _ := v.(map[any]any)
		if p, ok := ifaces[ifaceTransport].(map[any]any); ok {
			t := Transport{Path: path, UUID: str(p, "UUID"), State: str(p, "State")}
			t.Device, _ = prop(p, "Device").(dbus.ObjectPath)
			t.Address = AddressOf(t.Device)
			transports = append(transports, t)
		}
		if p, ok := ifaces[ifaceAdapter].(map[any]any); ok {
			adapters = append(adapters, Adapter{Path: path, Address: str(p, "Address"), Name: str(p, "Alias"),
				Powered: boolean(p, "Powered"), Discoverable: boolean(p, "Discoverable"), Pairable: boolean(p, "Pairable"),
				Discovering: boolean(p, "Discovering")})
		}
		if p, ok := ifaces[ifaceDevice].(map[any]any); ok {
			d := Device{Path: path, Address: str(p, "Address"), Name: str(p, "Name"), Alias: str(p, "Alias"), Icon: str(p, "Icon"),
				Paired: boolean(p, "Paired"), Bonded: boolean(p, "Bonded"), Trusted: boolean(p, "Trusted"), Connected: boolean(p, "Connected"),
				UUIDs: strs(p, "UUIDs")}
			d.Adapter, _ = prop(p, "Adapter").(dbus.ObjectPath)
			d.Class, _ = prop(p, "Class").(uint32)
			d.RSSI, _ = prop(p, "RSSI").(int16)
			devices = append(devices, d)
		}
	}
	sort.Slice(adapters, func(i, j int) bool { return adapters[i].Path < adapters[j].Path })
	sort.Slice(devices, func(i, j int) bool { return devices[i].Address < devices[j].Address })
	sort.Slice(transports, func(i, j int) bool { return transports[i].Path < transports[j].Path })
	return adapters, devices, transports
}

func parsePCMs(objs map[any]any) (pcms []PCM) {
	for k, v := range objs {
		path, _ := k.(dbus.ObjectPath)
		ifaces, _ := v.(map[any]any)
		p, ok := ifaces[ifacePCM].(map[any]any)
		if !ok {
			continue
		}
		pc := PCM{Path: path, Transport: str(p, "Transport"), Mode: str(p, "Mode"), Running: boolean(p, "Running"),
			Codec: str(p, "Codec"), SoftVolume: boolean(p, "SoftVolume")}
		pc.Device, _ = prop(p, "Device").(dbus.ObjectPath)
		pc.Address = AddressOf(pc.Device)
		pc.Format, _ = prop(p, "Format").(uint16)
		pc.Channels, _ = prop(p, "Channels").(byte)
		pc.Sampling, _ = prop(p, "Sampling").(uint32)
		pc.Delay, _ = prop(p, "Delay").(uint16)
		pc.Volume, _ = prop(p, "Volume").(uint16)
		pcms = append(pcms, pc)
	}
	sort.Slice(pcms, func(i, j int) bool { return pcms[i].Path < pcms[j].Path })
	return pcms
}

// ALSADevice — устройство ALSA для вывода в наушники с адресом addr: plug (подгоняет частоту и число каналов) поверх PCM
// BlueALSA. Кавычки обязательны: без них ALSA не разбирает запятую в аргументе (проверено на колонке 28.10.2026).
func ALSADevice(addr string) string { return "plug:'bluealsa:DEV=" + addr + ",PROFILE=a2dp'" }

// AddressOf — адрес устройства по пути BlueZ: …/dev_AA_BB_CC_DD_EE_FF → AA:BB:CC:DD:EE:FF.
func AddressOf(path dbus.ObjectPath) string {
	p := string(path)
	i := strings.LastIndex(p, "/dev_")
	if i < 0 {
		return ""
	}
	a := p[i+5:]
	if j := strings.IndexByte(a, '/'); j >= 0 {
		a = a[:j]
	}
	return strings.ReplaceAll(a, "_", ":")
}

// ValidAddress — адрес Bluetooth вида AA:BB:CC:DD:EE:FF.
func ValidAddress(a string) bool {
	if len(a) != 17 {
		return false
	}
	for i, r := range a {
		if i%3 == 2 {
			if r != ':' {
				return false
			}
			continue
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func prop(p map[any]any, name string) any {
	v, ok := p[name].(dbus.Variant)
	if !ok {
		return nil
	}
	return v.Value
}

func str(p map[any]any, name string) string { s, _ := prop(p, name).(string); return s }

func boolean(p map[any]any, name string) bool { b, _ := prop(p, name).(bool); return b }

func strs(p map[any]any, name string) []string {
	arr, _ := prop(p, name).([]any)
	var out []string
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func hasUUID(uuids []string, u string) bool {
	for _, x := range uuids {
		if strings.EqualFold(x, u) {
			return true
		}
	}
	return false
}
