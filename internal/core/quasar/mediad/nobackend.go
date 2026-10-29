package mediad

// NoBackend — Backend, который ничего не играет: подставляется, когда компонент музыки выключен или не вошёл в сборку
// (тогда Machine.NoMusic = true и команды музыки идут штатному mediad; автомату остаётся только следить за фокусом).
type NoBackend struct{}

func (NoBackend) Open(string, Track, uint64)   {}
func (NoBackend) Adopt(string, string, uint64) {}
func (NoBackend) Start(string)                 {}
func (NoBackend) Pause(string)                 {}
func (NoBackend) Close(string)                 {}
func (NoBackend) Seek(string, uint64)          {}
func (NoBackend) Duck(float64)                 {}
func (NoBackend) Pos(string) uint64            { return 0 }
func (NoBackend) Net(string) (uint64, uint64)  { return 0, 0 }
