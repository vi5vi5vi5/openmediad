package ringled

import (
	"strings"
	"testing"
	"time"

	"openmediad/internal/core/led"
)

// То, что лежит в прошивке: штатная пауза (сокращённо).
const pausePattern = `e800f3 e800f3 e800f3 e800f3 e800f3 e800f3 240
b100bc b100bc b100bb b100bc b100bc b100bb 40
260029 260029 260029 260029 260029 260029 40
000000 000000 000000 000000 000000 000000 1800
`

func TestParsePatternReadsFramesAndDurations(t *testing.T) {
	p, err := ParsePattern(strings.NewReader(pausePattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 4 || p.Loop || p.Gamma {
		t.Fatalf("кадров %d, loop %v, gamma %v", len(p.Steps), p.Loop, p.Gamma)
	}
	if want := (led.Color{R: 0xe8, G: 0x00, B: 0xf3}); p.Steps[0].Frame[0] != want || p.Steps[0].Frame[5] != want || p.Steps[0].Dur != 240*time.Millisecond {
		t.Fatalf("первый кадр: %+v", p.Steps[0])
	}
	if p.Total() != 2120*time.Millisecond {
		t.Fatalf("всего %v", p.Total())
	}
	// чёрный хвост кадров не считается: горит до конца третьего кадра
	if p.Lit() != 320*time.Millisecond {
		t.Fatalf("горит до %v, ждали 320ms", p.Lit())
	}
}

func TestParsePatternFlagsCommentsAndBlankLines(t *testing.T) {
	p, err := ParsePattern(strings.NewReader("gammacorrection            # необязательно\n\nloop\n# комментарий\n010203 000000 000000 000000 000000 000000 10  # кадр\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Loop || !p.Gamma || len(p.Steps) != 1 || p.Steps[0].Frame[0] != (led.Color{R: 1, G: 2, B: 3}) {
		t.Fatalf("%+v", p)
	}
}

func TestParsePatternRejectsBrokenFiles(t *testing.T) {
	for name, text := range map[string]string{
		"пусто":            "",
		"только флаги":     "loop\n",
		"мало цветов":      "000000 000000 10\n",
		"плохой цвет":      "zzzzzz 000000 000000 000000 000000 000000 10\n",
		"плохая длина":     "000000 000000 000000 000000 000000 000000 быстро\n",
		"отрицательная":    "000000 000000 000000 000000 000000 000000 -5\n",
		"незнакомая слово": "mirror\n",
	} {
		if _, err := ParsePattern(strings.NewReader(text)); err == nil {
			t.Errorf("%s: файл принят", name)
		}
	}
}

func TestPatternAtFollowsTheClock(t *testing.T) {
	p, _ := ParsePattern(strings.NewReader(pausePattern))
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	for at, want := range map[time.Duration]led.Color{
		0: {R: 0xe8, B: 0xf3}, ms(239): {R: 0xe8, B: 0xf3}, ms(240): {R: 0xb1, B: 0xbc}, ms(279): {R: 0xb1, B: 0xbc}, ms(280): {R: 0x26, B: 0x29}, ms(320): {},
	} {
		f, done := p.At(at)
		if done || f[0] != want {
			t.Errorf("на %v: %+v done=%v, ждали %+v", at, f[0], done, want)
		}
	}
	if _, done := p.At(p.Total()); !done {
		t.Fatal("после конца анимация не закончилась")
	}
	if f, done := p.At(time.Hour); !done || !f.IsZero() {
		t.Fatal("после конца кадр должен быть чёрным")
	}
}

func TestLoopedPatternNeverEnds(t *testing.T) {
	p, _ := ParsePattern(strings.NewReader("loop\n010101 010101 010101 010101 010101 010101 100\n"))
	if f, done := p.At(10*time.Hour + 50*time.Millisecond); done || f[0] != (led.Color{R: 1, G: 1, B: 1}) {
		t.Fatalf("зацикленная анимация закончилась: %+v %v", f, done)
	}
}

func TestFrameScale(t *testing.T) {
	f := Frame{0: led.Color{R: 200, G: 100, B: 50}}
	if f.Scale(255) != f {
		t.Fatal("255 — как в прошивке, кадр не меняется")
	}
	if got := f.Scale(128)[0]; got.R < 98 || got.R > 102 || got.G < 48 || got.G > 52 {
		t.Fatalf("половина яркости: %+v", got)
	}
	if !f.Scale(0).IsZero() {
		t.Fatal("яркость 0 — чёрный кадр")
	}
}

// Настоящие файлы прошивки (если тест идёт на колонке): все четыре анимации плеера разбираются.
func TestFirmwarePlayerPatternsParseWhenPresent(t *testing.T) {
	for _, name := range []string{PatternPause, PatternContinue, PatternNext, PatternPrevious} {
		p, err := LoadPattern(PatternDir, name)
		if err != nil {
			t.Skipf("файлов прошивки здесь нет: %v", err)
		}
		if p.Lit() <= 0 || p.Lit() > 3*time.Second {
			t.Errorf("%s: горит %v", name, p.Lit())
		}
	}
}
