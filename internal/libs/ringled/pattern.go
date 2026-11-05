package ringled

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"openmediad/internal/core/led"
)

// Анимации кольца прошивки. Файлы ledpatterns/oknix/*.led — текст: необязательные строки «gammacorrection» и «loop», остальные строки — кадры:
// шесть цветов RRGGBB (зоны 0…5) и длительность кадра в миллисекундах. Прошивка проигрывает их сама (player_pause.led …), когда пауза или
// смена трека идёт через её плеер; а когда пауза пришла из приложения Spotify, платформа о ней не знает. Тогда те же файлы проигрываем мы, и
// кольцо выглядит так же, как у штатной музыки. Файлы принадлежат прошивке и лежат на колонке; мы их только читаем и в сборку не кладём.

// PatternDir — каталог анимаций кольца в прошивке Мини 3.
const PatternDir = "/system/vendor/quasar/ledpatterns/oknix"

// Штатные анимации плеера.
const (
	PatternPause    = "player_pause.led"
	PatternContinue = "player_continue.led"
	PatternNext     = "player_next_track.led"
	PatternPrevious = "player_previous_track.led"
)

// Step — один кадр анимации.
type Step struct {
	Frame Frame
	Dur   time.Duration
}

// Pattern — анимация кольца.
type Pattern struct {
	Steps []Step
	Loop  bool // зациклить (в анимациях плеера нет)
	Gamma bool // файл просит гамма-коррекцию (в анимациях плеера нет, не применяется)
}

// ParsePattern разбирает файл анимации.
func ParsePattern(r io.Reader) (*Pattern, error) {
	p := &Pattern{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		switch {
		case len(f) == 0:
		case len(f) == 1 && f[0] == "loop":
			p.Loop = true
		case len(f) == 1 && f[0] == "gammacorrection":
			p.Gamma = true
		case len(f) == Zones+1:
			var st Step
			for i := 0; i < Zones; i++ {
				c, err := led.ParseColor(f[i])
				if err != nil {
					return nil, fmt.Errorf("строка %d: %w", n, err)
				}
				st.Frame[i] = c
			}
			ms, err := strconv.Atoi(f[Zones])
			if err != nil || ms < 0 {
				return nil, fmt.Errorf("строка %d: длительность %q — нужно целое число миллисекунд", n, f[Zones])
			}
			st.Dur = time.Duration(ms) * time.Millisecond
			p.Steps = append(p.Steps, st)
		default:
			return nil, fmt.Errorf("строка %d: ждали %d цветов и длительность, получили %q", n, Zones, strings.TrimSpace(line))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(p.Steps) == 0 {
		return nil, fmt.Errorf("в анимации нет кадров")
	}
	return p, nil
}

// LoadPattern читает анимацию из каталога dir (PatternDir на колонке).
func LoadPattern(dir, name string) (*Pattern, error) {
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	p, err := ParsePattern(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return p, nil
}

// Total — длительность анимации целиком.
func (p *Pattern) Total() time.Duration {
	var t time.Duration
	for _, s := range p.Steps {
		t += s.Dur
	}
	return t
}

// Lit — до какого момента на кольце что-то горит: хвост из чёрных кадров (штатная пауза держит кольцо чёрным ещё 1,8 с) нам ни к чему.
func (p *Pattern) Lit() time.Duration {
	var t, lit time.Duration
	for _, s := range p.Steps {
		t += s.Dur
		if !s.Frame.IsZero() {
			lit = t
		}
	}
	return lit
}

// At — кадр на момент elapsed от начала; done — анимация закончилась (у зацикленной не кончается).
func (p *Pattern) At(elapsed time.Duration) (f Frame, done bool) {
	total := p.Total()
	if total <= 0 {
		return Frame{}, true
	}
	if p.Loop {
		elapsed %= total
	} else if elapsed >= total {
		return Frame{}, true
	}
	var t time.Duration
	for _, s := range p.Steps {
		t += s.Dur
		if elapsed < t {
			return s.Frame, false
		}
	}
	return Frame{}, true
}

// Scale приглушает все кадры анимации до яркости b из 255 (255 — как в прошивке).
func (f Frame) Scale(b uint8) Frame {
	if b == 255 {
		return f
	}
	for i := range f {
		f[i] = f[i].Scale(b)
	}
	return f
}
