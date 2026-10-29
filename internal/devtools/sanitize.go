package devtools

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"

	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/pb"
	"openmediad/internal/core/quasar/wire"
	"openmediad/internal/devtools/capture"
)

// SanitizeOpt — что оставить в обезличенной записи.
type SanitizeOpt struct {
	KeepTitles bool // оставить названия, исполнителей и обложки треков (по умолчанию заменяются)
	KeepVoice  bool // оставить звук ответов Алисы (по умолчанию заменяется тишиной: в ответах бывают личные данные)
}

// SanitizeStats — что сделано.
type SanitizeStats struct {
	Frames, Dropped, Secrets int
}

// Sanitize превращает запись трафика (pcap или журнал кадров) в обезличенный журнал кадров для testdata/.
//
// Остаются только кадры плеера (audioclient) и спектра от плеера к iohub — их и сверяют золотые тесты. Секреты команд
// плееру (подписанные ссылки и зеркала, ключ потока, JSON директив, идентификаторы запросов, строка поля 40) заменяются во
// ВСЕХ кадрах строками той же длины: длины полей не меняются, кадры остаются корректными, а связи между командой и
// событием (URL события = URL команды + «?vsid=…») сохраняются. Буквы и цифры заменяются случайными, знаки (/ . : = ? & -)
// остаются: форма ссылок прежняя. Идентификаторы треков (номера каталога) остаются.
//
// Читает вход дважды (сначала собирает секреты, потом переписывает), поэтому нужен путь к файлу, а не поток.
func Sanitize(in string, out io.Writer, o SanitizeOpt) (SanitizeStats, error) {
	var st SanitizeStats
	secrets := map[string]bool{}
	err := capture.WalkFile(in, func(f capture.Frame) {
		if !keepFrame(f) {
			return
		}
		m, err := wire.Unwrap(f.Data)
		if err != nil || m.Type != wire.TypeAudioCommand {
			return
		}
		collectSecrets(msg.AudioCommand(m.Body), o, secrets)
	})
	if err != nil {
		return st, err
	}
	repl := replacements(secrets)
	st.Secrets = len(repl)
	w := capture.NewLogWriter(out)
	err = capture.WalkFile(in, func(f capture.Frame) {
		if !keepFrame(f) {
			st.Dropped++
			return
		}
		data := append([]byte(nil), f.Data...)
		for _, r := range repl {
			data = bytes.ReplaceAll(data, r.from, r.to)
		}
		if !o.KeepVoice {
			silenceVoice(data)
		}
		f.Data = data
		if werr := w.Write(f); werr != nil && err == nil {
			err = werr
		}
		st.Frames++
	})
	if err != nil {
		return st, err
	}
	return st, w.Close()
}

// keepFrame — кадры плеера и спектр от плеера к iohub.
func keepFrame(f capture.Frame) bool {
	return f.Svc == "audioclient" || f.Svc == "iohub_services" && f.ToSvc
}

// collectSecrets собирает секретные строки команды плееру.
func collectSecrets(c msg.AudioCommand, o SanitizeOpt, out map[string]bool) {
	add := func(b []byte) {
		if len(b) >= 12 { // короткие строки могли бы случайно встретиться в двоичных данных
			out[string(b)] = true
		}
	}
	if ref, ok := pb.Bytes(c, 40); ok {
		add(ref)
	}
	d, ok := pb.Bytes(c, 1)
	if !ok {
		return
	}
	r := pb.NewReader(d)
	for {
		f, err := r.Next()
		if err != nil {
			break
		}
		if f.Wire != pb.WireBytes {
			continue
		}
		switch f.Num {
		case 2, 42, 39, 7: // ссылка, зеркала, ключ, JSON директив
			add(f.B)
		case 8: // {1: id запроса, 2: «music», 3: id}
			if id, ok := pb.Bytes(f.B, 1); ok {
				add(id)
			}
			if id, ok := pb.Bytes(f.B, 3); ok {
				add(id)
			}
		case 9: // метаданные: название, исполнитель, обложка
			if o.KeepTitles {
				continue
			}
			for _, n := range []uint64{1, 2, 3} {
				if v, ok := pb.Bytes(f.B, n); ok && len(v) >= 3 {
					out[string(v)] = true
				}
			}
		}
	}
}

type replacement struct{ from, to []byte }

// replacements строит замены, самые длинные первыми: JSON директив содержит ссылки, и если заменить ссылку раньше,
// JSON целиком уже не найдётся.
func replacements(secrets map[string]bool) []replacement {
	out := make([]replacement, 0, len(secrets))
	for s := range secrets {
		out = append(out, replacement{from: []byte(s), to: []byte(scramble(s))})
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].from) != len(out[j].from) {
			return len(out[i].from) > len(out[j].from)
		}
		return bytes.Compare(out[i].from, out[j].from) < 0
	})
	return out
}

// scramble заменяет буквы и цифры случайными того же вида (шестнадцатеричная строка остаётся шестнадцатеричной), прочие
// символы оставляет. Длина в байтах не меняется: не-ASCII байты (кириллица) заменяются латиницей побайтно. У ссылок схема
// и хост остаются (хост CDN — не секрет, по нему журнал показывает, откуда шёл поток), заменяется путь с подписью.
func scramble(s string) string {
	hex := isHex(s)
	b := []byte(s)
	keep := 0
	for _, scheme := range []string{"https://", "http://"} {
		if strings.HasPrefix(s, scheme) {
			keep = len(scheme)
			if i := strings.IndexByte(s[keep:], '/'); i >= 0 {
				keep += i
			} else {
				keep = len(s)
			}
		}
	}
	for i, c := range b {
		if i < keep {
			continue
		}
		switch {
		case hex:
			b[i] = pick("0123456789abcdef")
		case c >= '0' && c <= '9':
			b[i] = pick("0123456789")
		case c >= 'a' && c <= 'z':
			b[i] = pick("abcdefghijklmnopqrstuvwxyz")
		case c >= 'A' && c <= 'Z':
			b[i] = pick("ABCDEFGHIJKLMNOPQRSTUVWXYZ")
		case c >= 0x80:
			b[i] = pick("abcdefghijklmnopqrstuvwxyz")
		}
	}
	return string(b)
}

func isHex(s string) bool {
	if len(s) < 16 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func pick(alphabet string) byte {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
	return alphabet[n.Int64()]
}

// silenceVoice заменяет содержимое голосовых чанков (поле 29 команды плееру) нулями той же длины: последовательность
// команд и размеры остаются, а сам ответ Алисы (в нём бывают личные данные) — нет.
func silenceVoice(frame []byte) {
	m, err := wire.Unwrap(frame)
	if err != nil || m.Type != wire.TypeAudioCommand {
		return
	}
	chunk, ok := msg.AudioCommand(m.Body).VoiceChunk()
	if !ok {
		return
	}
	clear(chunk) // срез указывает внутрь frame: заменяется на месте
}

// SanitizeReport — строка итога для человека.
func (s SanitizeStats) String() string {
	return fmt.Sprintf("кадров записано %d, отброшено %d, секретов заменено %d", s.Frames, s.Dropped, s.Secrets)
}
