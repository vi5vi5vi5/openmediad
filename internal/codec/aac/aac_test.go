package aac

import "testing"

func TestADTSHeader(t *testing.T) {
	h, err := ADTS([]byte{0x11, 0x90}, 371)
	if err != nil {
		t.Fatal(err)
	}
	// 48 кГц (индекс 3), 2 канала, LC: ff f1 4c 80 | длина 378 → 0x2f 0x5f 0xfc
	want := [7]byte{0xff, 0xf1, 0x4c, 0x80, 0x2f, 0x5f, 0xfc}
	if h != want {
		t.Fatalf("ADTS %x, ждали %x", h, want)
	}
	if _, err := ADTS([]byte{0x11}, 1); err == nil {
		t.Fatal("короткий ASC должен давать ошибку")
	}
	if _, err := ADTS([]byte{0x29, 0x90}, 1); err == nil { // объект 5 (HE-AAC) не поддерживается
		t.Fatal("HE-AAC без SBR-разбора должен отвергаться")
	}
}

func TestPipelineUsesOnlyElementsOfTheSpeaker(t *testing.T) {
	want := "fdsrc fd=0 ! aacparse ! avdec_aac ! audioconvert ! audioresample ! audio/x-raw,rate=48000,channels=2,format=S16LE ! fdsink fd=3 sync=false"
	if pipeline != want {
		t.Fatalf("конвейер изменился:\n%s\nпроверенный на колонке:\n%s", pipeline, want)
	}
}
