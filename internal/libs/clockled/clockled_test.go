package clockled

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestNamesFollowTheFrameOrder(t *testing.T) {
	if len(Names) != Segments || Segments != 32 {
		t.Fatalf("сегментов %d", len(Names))
	}
	for i, want := range map[int]string{0: "A0", 6: "G0", 7: "A1", 8: "B1", 9: "C1", 18: "E2", 19: "F2", 27: "G3", 28: "DH", 29: "DL", 30: "DM", 31: "DEGREE"} {
		if Names[i] != want {
			t.Errorf("сегмент %d: %q, ждали %q", i, Names[i], want)
		}
		if got, ok := Index(want); !ok || got != i {
			t.Errorf("Index(%q) = %d %v", want, got, ok)
		}
	}
	if _, ok := Index("Z9"); ok {
		t.Error("нашёлся несуществующий сегмент")
	}
}

func TestFrameScaleAndLit(t *testing.T) {
	var f Frame
	i, _ := Index("B1")
	f[i] = 200
	if f.Scale(255) != f {
		t.Fatal("255 — как в прошивке")
	}
	if got := f.Scale(128)[i]; got < 99 || got > 101 {
		t.Fatalf("половина: %d", got)
	}
	if !f.Scale(0).IsZero() {
		t.Fatal("яркость 0 — тёмный дисплей")
	}
	if lit := f.Lit(); len(lit) != 1 || lit[0] != "B1=c8" {
		t.Fatalf("горит: %v", lit)
	}
}

// stream собирает поток кадров так, как он лежит в файле прошивки.
func stream(loop bool, steps ...Step) []byte {
	var out []byte
	for i, s := range steps {
		flag := byte(0)
		if i == 0 && loop {
			flag = 1
		}
		ms := uint16(s.Dur / time.Millisecond)
		out = append(out, flag)
		out = append(out, s.Frame[:]...)
		out = append(out, byte(ms>>8), byte(ms))
	}
	return out
}

func seg(vals map[string]uint8) (f Frame) {
	for n, v := range vals {
		i, ok := Index(n)
		if !ok {
			panic(n)
		}
		f[i] = v
	}
	return f
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestParseAnimReadsFramesDurationsAndLoopFlag(t *testing.T) {
	raw := stream(false,
		Step{seg(nil), ms(20)},
		Step{seg(map[string]uint8{"B1": 0xff, "C1": 0xff, "E2": 0x80, "F2": 0x80}), ms(1700)}, // длительность больше 255: два байта
		Step{seg(nil), ms(520)},
	)
	a, err := ParseAnim(raw)
	if err != nil {
		t.Fatal(err)
	}
	if a.Loop || len(a.Steps) != 3 || a.Steps[1].Dur != ms(1700) || a.Steps[2].Dur != ms(520) {
		t.Fatalf("%+v", a)
	}
	if lit := a.Steps[1].Frame.Lit(); strings.Join(lit, " ") != "B1=ff C1=ff E2=80 F2=80" {
		t.Fatalf("горит: %v", lit)
	}
	if a.Total() != ms(2240) {
		t.Fatalf("всего %v", a.Total())
	}
	// чёрный хвост не считается: горит до конца второго кадра
	if a.Lit() != ms(1720) {
		t.Fatalf("горит до %v, ждали 1720ms", a.Lit())
	}
	if looped, _ := ParseAnim(stream(true, Step{seg(nil), ms(20)})); !looped.Loop {
		t.Fatal("флаг зацикленности не прочитан")
	}
}

func TestParseAnimRejectsBrokenStreams(t *testing.T) {
	for name, raw := range map[string][]byte{
		"пусто":           nil,
		"обрезан":         make([]byte, frameSize+3),
		"слишком длинный": stream(false, Step{Dur: 31 * time.Second}),
		"слишком много":   make([]byte, frameSize*(maxFrames+1)),
	} {
		if _, err := ParseAnim(raw); err == nil {
			t.Errorf("%s: принято", name)
		}
	}
}

func TestAtFollowsTheClock(t *testing.T) {
	a, _ := ParseAnim(stream(false,
		Step{seg(map[string]uint8{"A0": 1}), ms(20)},
		Step{seg(map[string]uint8{"A0": 2}), ms(40)},
	))
	for el, want := range map[time.Duration]uint8{0: 1, ms(19): 1, ms(20): 2, ms(59): 2} {
		f, done := a.At(el)
		if done || f[0] != want {
			t.Errorf("на %v: %d done=%v, ждали %d", el, f[0], done, want)
		}
	}
	if f, done := a.At(ms(60)); !done || !f.IsZero() {
		t.Fatal("после конца анимация не закончилась")
	}
}

// compress сжимает так же, как файлы прошивки (zstd).
func compress(t *testing.T, raw []byte) []byte {
	t.Helper()
	w, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	return w.EncodeAll(raw, nil)
}

func TestDecodeAndLoadAnimFromACompressedFile(t *testing.T) {
	raw := stream(false, Step{seg(map[string]uint8{"B1": 0xff}), ms(20)}, Step{seg(nil), ms(500)})
	a, err := DecodeAnim(compress(t, raw))
	if err != nil || len(a.Steps) != 2 {
		t.Fatalf("%v %+v", err, a)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, AnimPause+".binscled.zst"), compress(t, raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if loaded, err := LoadAnim(dir, AnimPause); err != nil || len(loaded.Steps) != 2 {
		t.Fatalf("LoadAnim: %v", err)
	}
	if _, err := LoadAnim(dir, AnimNext); err == nil {
		t.Fatal("несуществующий файл загружен")
	}
	if _, err := DecodeAnim([]byte("не zstd")); err == nil {
		t.Fatal("мусор принят за анимацию")
	}
}

// Настоящие файлы прошивки (на колонке или в каталоге OMD_CLOCK_ANIMS): форма анимаций плеера та, что видна на дисплее.
func TestFirmwarePlayerAnimationsHaveTheShapeSeenOnTheDisplay(t *testing.T) {
	dir := os.Getenv("OMD_CLOCK_ANIMS")
	if dir == "" {
		dir = AnimDir
	}
	pause, err := LoadAnim(dir, AnimPause)
	if errors.Is(err, os.ErrNotExist) {
		t.Skipf("файлов прошивки здесь нет: %v", err)
	}
	if err != nil {
		t.Fatalf("файл прошивки не читается: %v", err)
	}
	// пауза — две вертикали вокруг двоеточия: B1 C1 (справа от второй цифры) и E2 F2 (слева от третьей)
	peak := pause.Steps[14].Frame
	if got := strings.Join(peak.Lit(), " "); got != "B1=ff C1=ff E2=ff F2=ff" {
		t.Errorf("пик паузы: %s", got)
	}
	if pause.Steps[14].Dur != ms(1700) || pause.Loop {
		t.Errorf("пауза держится %v, зациклена: %v", pause.Steps[14].Dur, pause.Loop)
	}
	next, err := LoadAnim(dir, AnimNext)
	if err != nil {
		t.Fatal(err)
	}
	prev, err := LoadAnim(dir, AnimPrevious)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Steps) != len(prev.Steps) {
		t.Fatalf("кадров: дальше %d, назад %d", len(next.Steps), len(prev.Steps))
	}
	// «дальше» идёт от центра вправо, «назад» — зеркально влево: в каждом кадре горят зеркальные сегменты (яркости у прошивки в кадрах не зеркальны:
	// ведущая полоса «назад» держится, а «дальше» гаснет). Зеркало: правая вертикаль цифры 2 ↔ левая вертикаль цифры 1, цифры 3 ↔ 0.
	mirror := map[string]string{"B2": "E1", "C2": "F1", "E2": "B1", "F2": "C1", "B3": "E0", "C3": "F0", "E3": "B0", "F3": "C0"}
	for i := range next.Steps {
		for from, to := range mirror {
			fi, _ := Index(from)
			ti, _ := Index(to)
			if (next.Steps[i].Frame[fi] != 0) != (prev.Steps[i].Frame[ti] != 0) {
				t.Fatalf("кадр %d: у «дальше» %s=%02x, а у «назад» зеркальный %s=%02x", i, from, next.Steps[i].Frame[fi], to, prev.Steps[i].Frame[ti])
			}
		}
	}
	if next.Lit() < ms(800) || next.Lit() > ms(1000) {
		t.Errorf("перемотка горит %v", next.Lit())
	}
}
