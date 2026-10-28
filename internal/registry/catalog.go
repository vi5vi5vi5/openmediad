package registry

// Каталог — все компоненты проекта, вошедшие в сборку или нет. Это единственное место, где задано, какие компоненты
// бывают, какие из них входят в сборку по умолчанию и каким тегом это меняется:
//   - компонент «по умолчанию» (Default) входит в сборку, пока не задан тег omd_no_<имя>;
//   - опытный компонент входит в сборку только с тегом omd_<имя>.
//
// Файлы cmd/openmediad/with_<имя>.go должны соответствовать каталогу; это проверяет тест в cmd/openmediad.
// tools/build берёт отсюда список компонентов для -with, -without и -list.

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

// Info — запись каталога.
type Info struct {
	Name    string
	Section string // секция файла настроек (пусто — у компонента нет настроек)
	Kind    Kind
	Summary string
	Default bool // входит в сборку по умолчанию
	Ready   bool // реализован; нереализованные видны только в `openmediad components` как план
	Phase   int  // фаза плана, в которой появляется (для нереализованных)
}

// Catalog — все компоненты по порядку показа.
var Catalog = []Info{
	{Name: "yandex", Section: "source.yandex", Kind: Source, Summary: "музыка Яндекса из команд платформы", Default: true, Ready: true},
	{Name: "tts", Section: "source.tts", Kind: Source, Summary: "голос Алисы", Default: true, Ready: true},
	{Name: "sounds", Section: "source.sounds", Kind: Source, Summary: "звонки таймеров и будильников, системные звуки", Default: true, Ready: true},
	{Name: "spectrum", Section: "spectrum", Kind: Service, Summary: "спектр звука для дисплея и подсветки (iohub)", Default: true, Ready: true},
	{Name: "debug", Section: "debug", Kind: Service, Summary: "отладочный веб-порт с журналом (только чтение)", Default: true, Ready: true},
	{Name: "pprof", Kind: Service, Summary: "профилировщик Go в отладочном порту (для разработки)", Ready: true},
	{Name: "bluetooth", Section: "sink.bluetooth", Kind: Sink, Summary: "Bluetooth-наушники", Default: true, Phase: 6},
	{Name: "netout", Section: "sink.netout", Kind: Sink, Summary: "звук в сеть для других программ", Phase: 7},
	{Name: "radio", Section: "source.radio", Kind: Source, Summary: "интернет-радио и HTTP-потоки", Phase: 8},
	{Name: "spotify", Section: "source.spotify", Kind: Source, Summary: "Spotify (librespot)", Phase: 9},
	{Name: "soundcloud", Section: "source.soundcloud", Kind: Source, Summary: "SoundCloud", Phase: 10},
	{Name: "netin", Section: "source.netin", Kind: Source, Summary: "приём звука по сети", Phase: 11},
	{Name: "btin", Section: "source.btin", Kind: Source, Summary: "приём звука с телефона по Bluetooth", Phase: 11},
}

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

// Tag — тег сборки, который меняет вхождение компонента: omd_no_<имя> у компонентов по умолчанию, omd_<имя> у опытных.
func (i Info) Tag() string {
	if i.Default {
		return "omd_no_" + i.Name
	}
	return "omd_" + i.Name
}

// InBuild — войдёт ли компонент в сборку с такими тегами.
func (i Info) InBuild(tags []string) bool {
	if !i.Ready {
		return false
	}
	has := false
	for _, t := range tags {
		if t == i.Tag() {
			has = true
		}
	}
	if i.Default {
		return !has
	}
	return has
}
