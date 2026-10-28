package capture

import (
	"bytes"
	"testing"

	"openmediad/internal/quasar/wire"
)

const uuid = "4a096101-0114-46eb-b935-d27be0eb3ba1"

func collect() (func([]byte), *[][]byte) {
	var got [][]byte
	return func(b []byte) { got = append(got, append([]byte(nil), b...)) }, &got
}

func TestFlowSplitSegments(t *testing.T) {
	a, b := wire.CancelDialog(), wire.PausePlayback(uuid, "Keyboard")
	stream := wire.AppendFrame(wire.AppendFrame(nil, a), b)
	emit, got := collect()

	f := &Flow{}
	f.Add(1000, 0x02, nil, emit) // SYN
	seq := uint32(1001)
	for _, cut := range []int{3, 10, 30, len(stream)} { // режем посреди заголовка и посреди кадров
		start := int(seq - 1001)
		f.Add(seq, 0x18, stream[start:cut], emit)
		seq = 1001 + uint32(cut)
	}
	if len(*got) != 2 || !bytes.Equal((*got)[0], a) || !bytes.Equal((*got)[1], b) {
		t.Fatalf("получили %d кадров", len(*got))
	}
}

func TestFlowRetransmit(t *testing.T) {
	a := wire.CancelDialog()
	frame := wire.AppendFrame(nil, a)
	emit, got := collect()
	f := &Flow{}
	f.Add(1000, 0x02, nil, emit)
	f.Add(1001, 0x18, frame[:10], emit)
	f.Add(1001, 0x18, frame[:10], emit) // полный повтор
	f.Add(1001, 0x18, frame[:20], emit) // частичный повтор с продолжением
	f.Add(1021, 0x18, frame[20:], emit)
	if len(*got) != 1 || !bytes.Equal((*got)[0], a) {
		t.Fatalf("получили %d кадров", len(*got))
	}
}

func TestFlowResyncAfterLoss(t *testing.T) {
	a, b := wire.CancelDialog(), wire.PausePlayback(uuid, "Keyboard")
	fa, fb := wire.AppendFrame(nil, a), wire.AppendFrame(nil, b)
	emit, got := collect()
	f := &Flow{}
	f.Add(1000, 0x02, nil, emit)
	f.Add(1001, 0x18, fa[:5], emit) // кадр a не дошёл целиком
	// дыра в потоке: следующий сегмент начинается с границы кадра b
	f.Add(1001+uint32(len(fa))+7, 0x18, fb, emit)
	if len(*got) != 1 || !bytes.Equal((*got)[0], b) {
		t.Fatalf("после дыры должен разобраться кадр b, получили %d кадров", len(*got))
	}
}

func TestFlowGarbageDoesNotSync(t *testing.T) {
	emit, got := collect()
	f := &Flow{}
	f.Add(5, 0x18, []byte{0x10, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, emit)
	if len(*got) != 0 {
		t.Fatal("мусор не должен давать кадров")
	}
}
