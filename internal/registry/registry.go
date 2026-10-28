// Package registry — реестр компонентов, вошедших в сборку.
//
// Компонент регистрируется в init() своего register.go (и больше ничего там не делает: никакой работы при запуске).
// В бинарник компонент попадает через файл cmd/openmediad/with_<имя>.go с тегом сборки (см. catalog.go). Демон
// создаёт только те компоненты, что включены в настройках.
package registry

import (
	"fmt"
	"sync"
)

// Component — компонент, вошедший в сборку.
type Component struct {
	Name string
	// Defaults возвращает указатель на структуру настроек с умолчаниями (теги полей `conf:"ключ"`; config.Toggle даёт ключ
	// enabled); nil — без настроек. Секция настроек — из каталога (Info.Section).
	Defaults func() any
	// Template — секция для файла настроек по умолчанию: заголовок и ключи с комментариями (пусто — без секции).
	Template string
	// New создаёт компонент. cfg — то, что вернул Defaults, после чтения файла настроек. Результат — объект, который демон
	// подключает по интерфейсам, которые он реализует (см. internal/app).
	New func(env *Env, cfg any) (any, error)
}

var (
	mu   sync.Mutex
	regs = map[string]Component{}
)

// Register добавляет компонент. Имя должно быть в каталоге; повтор — ошибка программиста.
func Register(c Component) {
	info, ok := Lookup(c.Name)
	if !ok {
		panic(fmt.Sprintf("registry: компонента %q нет в каталоге", c.Name))
	}
	if (c.Defaults != nil) != (info.Section != "") {
		panic(fmt.Sprintf("registry: у компонента %q настройки должны быть тогда и только тогда, когда в каталоге есть секция", c.Name))
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := regs[c.Name]; dup {
		panic(fmt.Sprintf("registry: компонент %q зарегистрирован дважды", c.Name))
	}
	regs[c.Name] = c
}

// Get возвращает компонент сборки по имени.
func Get(name string) (Component, bool) {
	mu.Lock()
	defer mu.Unlock()
	c, ok := regs[name]
	return c, ok
}

// Compiled — имена компонентов, вошедших в сборку, в порядке каталога.
func Compiled() []string {
	mu.Lock()
	defer mu.Unlock()
	var out []string
	for _, c := range Catalog {
		if _, ok := regs[c.Name]; ok {
			out = append(out, c.Name)
		}
	}
	return out
}

// Missing — готовые компоненты каталога, которые в эту сборку не вошли.
func Missing() []string {
	mu.Lock()
	defer mu.Unlock()
	var out []string
	for _, c := range Catalog {
		if _, ok := regs[c.Name]; !ok && c.Ready {
			out = append(out, c.Name)
		}
	}
	return out
}

// All — компоненты сборки в порядке каталога.
func All() []Component {
	mu.Lock()
	defer mu.Unlock()
	var out []Component
	for _, c := range Catalog {
		if r, ok := regs[c.Name]; ok {
			out = append(out, r)
		}
	}
	return out
}
