package registry

// Env — то, что демон даёт компонентам при создании. Поля заполняет internal/app; компонент берёт только нужное.
type Env struct {
	// Logf возвращает функцию журнала для источника src (например «music»): запись уровня info без пояснения.
	Logf func(src string) func(format string, a ...any)
	// Services — общие службы демона по имени интерфейса (движок, автомат mediad, журнал…). Заполняет internal/app;
	// компонент приводит значение к нужному типу. Так реестр не зависит от пакетов, которые его используют.
	Services map[string]any
}

// Use достаёт общую службу по имени; ok=false, если её нет или она другого типа.
func Use[T any](env *Env, name string) (T, bool) {
	var zero T
	if env == nil || env.Services == nil {
		return zero, false
	}
	v, ok := env.Services[name].(T)
	return v, ok
}
