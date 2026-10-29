package registry

import "strings"

// Паспорт компонента — единая запись о нём: как называется, насколько важен, входит ли в сборку по умолчанию, что требует
// от прошивки и от других компонентов, что будет, если его убрать. Сами паспорта лежат в internal/components/catalog; ядро
// знает только тип записи и получает список при запуске (SetCatalog) от cmd/openmediad: в коде ядра нет ни одного имени
// компонента (это проверяет internal/architecture_test.go).

// Kind — вид компонента.
type Kind int

const (
	Source  Kind = iota // источник звука
	Sink                // выход звука
	Service             // служба: отладочный порт, визуализация и т. п.
)

func (k Kind) String() string {
	switch k {
	case Source:
		return "источник"
	case Sink:
		return "выход"
	}
	return "служба"
}

// Class — насколько компонент важен.
type Class int

const (
	Necessary Class = iota // необходимый: то, ради чего проект существует
	Standard               // штатный: повторяет штатное поведение колонки
	Auxiliary              // вспомогательный: для людей, не для звука (отладка, разработка)
	Optional               // необязательный: новая возможность сверх штатной колонки
)

func (c Class) String() string {
	switch c {
	case Necessary:
		return "необходимый"
	case Standard:
		return "штатный"
	case Auxiliary:
		return "вспомогательный"
	}
	return "необязательный"
}

// Идентификаторы того, что компоненты требуют от прошивки и окружения. `openmediad doctor` умеет проверять часть из них. Это
// факты о колонке, а не имена компонентов, поэтому они живут в ядре.
const (
	NeedGStreamer   = "gstreamer"       // maind --gst-launch
	NeedAplay       = "aplay"           // системный aplay
	NeedArecord     = "arecord"         // системный arecord
	NeedCA          = "ca-certificates" // корневые сертификаты прошивки
	NeedSoundFiles  = "sound-files"     // системные звуки /system/vendor/quasar/sounds/
	NeedIOHub       = "iohub"           // сервис iohub платформы, порт 9903
	NeedAliced      = "aliced"          // сервис aliced платформы, порт 9875
	NeedBluez       = "bluez"           // BlueZ и BlueALSA на системной шине D-Bus
	NeedBluealsaRun = "bluealsa-aplay"  // штатная служба bluealsa-aplay (init-скрипт S53)
	NeedInput       = "input-events"    // /dev/input: кнопки наушников
	NeedMaster      = "alsa-master"     // регулятор громкости ALSA Master
	NeedNetwork     = "network"         // сеть
)

// Need — что компоненту нужно от прошивки и окружения. ID знает `openmediad doctor` (он умеет проверять часть из них),
// Text — то же словами для человека.
type Need struct {
	ID   string
	Text string
}

// Info — паспорт компонента.
type Info struct {
	Name    string // единое имя: папка, секция настроек, имя в -with и -without; тег сборки — omd_ + имя с «_» вместо «-»
	Title   string // название по-русски
	Section string // секция файла настроек (пусто — у компонента нет настроек)
	Kind    Kind
	Class   Class
	Summary string // что делает

	Default      bool // входит в профиль сборки standard
	Experimental bool // проверен не полностью
	Ready        bool // реализован; нереализованные видны только как план
	Phase        int  // фаза плана, в которой появляется (для нереализованных)

	Needs           []Need   // что нужно от прошивки и окружения
	NeedsComponents []string // другие компоненты, без которых этот не работает
	WithoutIt       string   // что изменится, если компонента нет
}

// Tag — тег сборки, включающий компонент: omd_ и имя, где «-» заменён на «_» (Go не допускает дефис в теге).
func (i Info) Tag() string { return "omd_" + strings.ReplaceAll(i.Name, "-", "_") }

// InBuild — войдёт ли компонент в сборку с такими тегами.
func (i Info) InBuild(tags []string) bool {
	if !i.Ready {
		return false
	}
	for _, t := range tags {
		if t == i.Tag() {
			return true
		}
	}
	return false
}

// Catalog — паспорта всех компонентов по порядку показа. Заполняется SetCatalog.
var Catalog []Info

// SetCatalog задаёт каталог. Вызывается один раз при запуске (internal/components/catalog).
func SetCatalog(list []Info) { Catalog = list }

// BySection ищет компонент каталога по секции настроек.
func BySection(section string) (Info, bool) {
	for _, c := range Catalog {
		if c.Section != "" && c.Section == section {
			return c, true
		}
	}
	return Info{}, false
}

// Lookup ищет компонент каталога по имени.
func Lookup(name string) (Info, bool) {
	for _, c := range Catalog {
		if c.Name == name {
			return c, true
		}
	}
	return Info{}, false
}
