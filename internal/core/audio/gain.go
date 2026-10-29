package audio

import (
	"encoding/binary"
	"math"
)

// RampStep — на сколько громкость может сдвинуться за одну порцию вывода (≈50 мс): так приглушение на время диалога и
// возврат громкости идут плавно, без щелчка.
const RampStep = 0.25

// ApplyGain умножает отсчёты S16LE на громкость, плавно меняя её от cur к target в пределах порции (не больше RampStep за
// порцию). Возвращает громкость к концу порции. Арифметика целочисленная (Q16 и Q32): на колонке вариант с math.Max/Round на
// каждый отсчёт съедал около 3 % ядра (замер профилировщиком, docs/research/HISTORY.md, раздел 18).
func ApplyGain(b []byte, cur, target float64) float64 {
	if cur == 1 && target == 1 {
		return 1
	}
	n := len(b) / 2
	if n == 0 {
		return cur
	}
	next := target
	if d := target - cur; math.Abs(d) > RampStep {
		next = cur + math.Copysign(RampStep, d)
	}
	if next == cur { // постоянная громкость: обычный случай
		g := int32(math.Round(cur * 65536))
		for i := 0; i < n; i++ {
			put16(b[2*i:], scale16(int32(int16(binary.LittleEndian.Uint16(b[2*i:]))), g))
		}
		return next
	}
	g := int64(cur * (1 << 32))                          // громкость Q32 в начале порции
	step := int64((next - cur) * (1 << 32) / float64(n)) // шаг на отсчёт
	for i := 0; i < n; i++ {
		put16(b[2*i:], scale16(int32(int16(binary.LittleEndian.Uint16(b[2*i:]))), int32(g>>16)))
		g += step
	}
	return next
}

// scale16 умножает отсчёт на громкость в Q16 с округлением и ограничением по диапазону int16.
func scale16(v, g int32) int16 {
	x := (int64(v)*int64(g) + 32768) >> 16
	switch {
	case x > 32767:
		return 32767
	case x < -32768:
		return -32768
	}
	return int16(x)
}

func put16(b []byte, v int16) { binary.LittleEndian.PutUint16(b, uint16(v)) }

// FadeIn плавно поднимает громкость от нуля: total — длина всего нарастания в байтах, left — сколько его ещё осталось
// (в начале total). Возвращает, сколько осталось после этой порции; нарастание можно применять порциями любого размера.
func FadeIn(b []byte, left, total int) int {
	n := len(b) / 2
	all := total / 2
	rem := left / 2
	for i := 0; i < n && rem > 0; i++ {
		g := 1 - float64(rem)/float64(all)
		v := float64(int16(binary.LittleEndian.Uint16(b[2*i:]))) * g
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(math.Round(v))))
		rem--
	}
	return rem * 2
}

// DB — множитель громкости для поправки в децибелах.
func DB(db float64) float64 { return math.Pow(10, db/20) }
