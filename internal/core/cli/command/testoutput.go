package command

import (
	"sort"
	"sync"

	"openmediad/internal/core/audio"
)

// TestOutput открывает выход для `openmediad test-sound ИМЯ` мимо демона. aplayPath — системный aplay из настроек.
type TestOutput func(aplayPath string, f audio.Format) (audio.Output, error)

var (
	toMu        sync.Mutex
	testOutputs = map[string]TestOutput{}
)

// RegisterTestOutput добавляет пробный выход (компонент-выход делает это в init()); так `test-sound` не знает ни об одном
// компоненте по имени.
func RegisterTestOutput(name string, open TestOutput) {
	toMu.Lock()
	defer toMu.Unlock()
	testOutputs[name] = open
}

// LookupTestOutput ищет пробный выход по имени.
func LookupTestOutput(name string) (TestOutput, bool) {
	toMu.Lock()
	defer toMu.Unlock()
	o, ok := testOutputs[name]
	return o, ok
}

// TestOutputNames — имена зарегистрированных пробных выходов по алфавиту.
func TestOutputNames() []string {
	toMu.Lock()
	defer toMu.Unlock()
	names := make([]string, 0, len(testOutputs))
	for n := range testOutputs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
