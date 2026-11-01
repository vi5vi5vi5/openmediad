package daemon

import (
	"sync"

	"openmediad/internal/core/audio"
	"openmediad/internal/core/led"
)

// ledColorer — выход сам предлагает свой цвет для подсветки (наушники — синий).
type ledColorer interface{ LEDColor() led.Color }

// indicate сообщает панели индикации, что музыка пошла в выход name, и снимает сообщение, когда последний музыкальный поток этого выхода кончился
// (доигран, оборван паузой или закрыт). Голос Алисы и звонки не считаются: подсветка показывает, куда идёт музыка.
func (rt *router) indicate(name string, s Sink, out audio.Output) audio.Output {
	var def led.Color
	if lc, ok := s.(ledColorer); ok {
		def = lc.LEDColor()
	}
	rt.imu.Lock()
	if rt.refs == nil {
		rt.refs = map[string]int{}
	}
	rt.refs[name]++
	if rt.refs[name] == 1 {
		rt.ind.On(led.Output, name, def)
	}
	rt.imu.Unlock()
	return &indicatedOutput{Output: out, release: sync.OnceFunc(func() {
		rt.imu.Lock()
		defer rt.imu.Unlock()
		if rt.refs[name]--; rt.refs[name] <= 0 {
			delete(rt.refs, name)
			rt.ind.Off(led.Output, name)
		}
	})}
}

// indicatedOutput — вывод, который по окончании снимает отметку на панели индикации.
type indicatedOutput struct {
	audio.Output
	release func()
}

func (o *indicatedOutput) Drain() { o.Output.Drain(); o.release() }
func (o *indicatedOutput) Abort() { o.Output.Abort(); o.release() }
