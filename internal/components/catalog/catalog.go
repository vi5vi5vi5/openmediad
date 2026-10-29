// Package catalog — единая таблица паспортов компонентов: какие бывают, насколько важны, входят ли в сборку по умолчанию, что
// требуют от прошивки и друг от друга. Это единственное место, где задан список компонентов; подробности для людей —
// docs/COMPONENTS.md и README.md в папке каждого компонента (их переписывает `go run ./tools/build -sync-docs`).
//
// Пакет не импортирует код компонентов — только описывает их, поэтому его можно подключать откуда угодно (в том числе в
// tools/build, где компоненты не нужны). В init() он передаёт список ядру: registry.SetCatalog.
//
// Правила имён: единое имя компонента — это папка в internal/components, секция файла настроек, имя в -with и -without;
// тег сборки — omd_ и то же имя с «_» вместо «-».
package catalog

import (
	"fmt"

	"openmediad/internal/core/registry"
)

func init() { registry.SetCatalog(All) }

func need(id, text string) registry.Need { return registry.Need{ID: id, Text: text} }

// All — паспорта всех компонентов по порядку показа: сначала готовые, потом запланированные.
var All = []registry.Info{
	{
		Name: "yandex-music", Title: "Музыка Яндекса", Section: "yandex-music", Kind: registry.Source, Class: registry.Standard,
		Summary: "играет музыку Яндекса из команд платформы сам: HTTPS, расшифровка, перемотка, спектр для дисплея",
		Default: true, Ready: true,
		Needs: []registry.Need{
			need(registry.NeedGStreamer, "GStreamer колонки (`maind --gst-launch`): декодирует AAC"),
			need(registry.NeedAplay, "системный `aplay`: вывод звука"),
			need(registry.NeedCA, "корневые сертификаты прошивки `/system/vendor/quasar/ca-certificates.crt` (запасной набор)"),
			need(registry.NeedNetwork, "сеть (HTTPS до серверов Яндекса)"),
		},
		WithoutIt: "музыку Яндекса играет штатный mediad",
	},
	{
		Name: "alice-voice", Title: "Голос Алисы", Section: "alice-voice", Kind: registry.Source, Class: registry.Standard,
		Summary: "играет ответы Алисы (Ogg/Opus) своим движком; декодер на чистом Go, GStreamer не нужен",
		Default: true, Ready: true,
		Needs:     []registry.Need{need(registry.NeedAplay, "системный `aplay`: вывод звука")},
		WithoutIt: "ответы Алисы говорит штатный mediad",
	},
	{
		Name: "system-sounds", Title: "Звонки и системные звуки", Section: "system-sounds", Kind: registry.Source, Class: registry.Standard,
		Summary: "звонки таймеров и будильников по кругу и одноразовые системные звуки (после «Алиса», щелчок громкости)",
		Default: true, Ready: true,
		Needs: []registry.Need{
			need(registry.NeedGStreamer, "GStreamer колонки (`maind --gst-launch`): раскодирует файлы звуков"),
			need(registry.NeedAplay, "системный `aplay`: вывод звука"),
			need(registry.NeedSoundFiles, "файлы звуков прошивки `/system/vendor/quasar/sounds/`"),
		},
		WithoutIt: "звонки и системные сигналы играет штатный mediad",
	},
	{
		Name: "display-spectrum", Title: "Спектр для дисплея и подсветки", Section: "display-spectrum", Kind: registry.Service, Class: registry.Standard,
		Summary: "отправляет платформе спектр звука, который движок играет сам: анимация частот на дисплее и подсветка",
		Default: true, Ready: true,
		Needs:     []registry.Need{need(registry.NeedIOHub, "сервис `iohub` платформы (127.0.0.1:9903)")},
		WithoutIt: "анимации частот на дисплее и подсветки при музыке и голосе движка нет",
	},
	{
		Name: "debug-page", Title: "Отладочная веб-страница", Section: "debug-page", Kind: registry.Service, Class: registry.Auxiliary,
		Summary: "веб-страница с живым журналом и пояснениями (только чтение), порт 6701",
		Default: true, Ready: true,
		WithoutIt: "нет страницы с живым журналом (`openmediad logs` работает)",
	},
	{
		Name: "debug-profiler", Title: "Профилировщик Go", Kind: registry.Service, Class: registry.Auxiliary,
		Summary: "профилировщик pprof на порту отладочной страницы (для разработки)",
		Ready:   true, NeedsComponents: []string{"debug-page"},
		WithoutIt: "нельзя снять профиль процессора и памяти демона",
	},
	{
		Name: "bt-out", Title: "Bluetooth-наушники: вывод звука", Section: "bt-out", Kind: registry.Sink, Class: registry.Optional,
		Summary:      "колонка играет в Bluetooth-наушники (A2DP): музыку, голос и звонки по маршруту `[route]`, кнопки и громкость наушников; команда `bt`",
		Experimental: true, Ready: true,
		Needs: []registry.Need{
			need(registry.NeedBluez, "BlueZ и BlueALSA на системной шине D-Bus"),
			need(registry.NeedAplay, "системный `aplay` с плагином bluealsa"),
			need(registry.NeedInput, "`/dev/input`: кнопки наушников (AVRCP)"),
			need(registry.NeedAliced, "сервис `aliced` платформы (127.0.0.1:9875): кнопки наушников → платформе"),
			need(registry.NeedMaster, "регулятор громкости ALSA Master"),
		},
		WithoutIt: "звук только на динамиках",
	},
	{
		Name: "bt-in", Title: "Bluetooth: звук с телефона через движок", Section: "bt-in", Kind: registry.Source, Class: registry.Optional,
		Summary:      "звук с телефона по Bluetooth идёт через движок (приглушение, спектр, маршрут), а не через штатный bluealsa-aplay; команда `bt`",
		Experimental: true, Ready: true,
		Needs: []registry.Need{
			need(registry.NeedBluez, "BlueZ и BlueALSA на системной шине D-Bus"),
			need(registry.NeedArecord, "системный `arecord`: чтение PCM телефона"),
			need(registry.NeedBluealsaRun, "штатная служба `bluealsa-aplay` (`/etc/init.d/S53bluealsa-aplay`): на время работы компонента останавливается"),
		},
		WithoutIt: "звук телефона играет штатный bluealsa-aplay",
	},

	// запланированные (в сборку не входят)
	{Name: "net-out", Title: "Звук в сеть", Section: "net-out", Kind: registry.Sink, Class: registry.Optional, Summary: "звук в сеть для других программ", Phase: 7},
	{Name: "internet-radio", Title: "Интернет-радио", Section: "internet-radio", Kind: registry.Source, Class: registry.Optional, Summary: "интернет-радио и HTTP-потоки", Phase: 8},
	{Name: "spotify", Title: "Spotify", Section: "spotify", Kind: registry.Source, Class: registry.Optional, Summary: "Spotify (librespot)", Phase: 9},
	{Name: "soundcloud", Title: "SoundCloud", Section: "soundcloud", Kind: registry.Source, Class: registry.Optional, Summary: "SoundCloud", Phase: 10},
	{Name: "net-in", Title: "Приём звука по сети", Section: "net-in", Kind: registry.Source, Class: registry.Optional, Summary: "приём звука по сети", Phase: 11},
}

// Профили сборки.
const (
	ProfileCore     = "core"     // только ядро: прозрачный посредник, колонка играет штатно
	ProfileStandard = "standard" // компоненты «по умолчанию»
	ProfileFull     = "full"     // все готовые компоненты, включая опытные
)

// Profiles — имена профилей по порядку.
var Profiles = []string{ProfileCore, ProfileStandard, ProfileFull}

// Profile возвращает имена компонентов профиля в порядке каталога.
func Profile(name string) ([]string, error) {
	var out []string
	switch name {
	case ProfileCore:
	case ProfileStandard:
		for _, c := range All {
			if c.Ready && c.Default {
				out = append(out, c.Name)
			}
		}
	case ProfileFull:
		for _, c := range All {
			if c.Ready {
				out = append(out, c.Name)
			}
		}
	default:
		return nil, fmt.Errorf("нет профиля %q (есть: core, standard, full)", name)
	}
	return out, nil
}

// Lookup ищет паспорт по имени.
func Lookup(name string) (registry.Info, bool) {
	for _, c := range All {
		if c.Name == name {
			return c, true
		}
	}
	return registry.Info{}, false
}
