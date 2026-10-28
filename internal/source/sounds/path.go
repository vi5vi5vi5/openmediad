// Package sounds — источник «звонки и системные звуки»: локальные файлы прошивки (звонок таймера и будильника) раскодируются
// целиком в память и играют по кругу, пока платформа не пришлёт release. И привязка к эмуляции mediad (bind.go).
package sounds

import "strings"

// Dir — где лежат системные звуки колонки (таймер, будильник, сигналы).
const Dir = "/system/vendor/quasar/sounds/"

// TimerSound — звонок таймера и будильника по умолчанию (по имени «wav», по содержимому MP3 64 кбит/с, ≈9,27 с).
const TimerSound = Dir + "common/timer.wav"

// ValidPath сообщает, можно ли играть этот файл: только внутри Dir и только из безопасных символов, потому что путь
// подставляется в строку запуска GStreamer. Всё остальное играет штатный mediad.
func ValidPath(p string) bool {
	if !strings.HasPrefix(p, Dir) || len(p) == len(Dir) || strings.Contains(p, "..") {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-', r == '/':
		default:
			return false
		}
	}
	return true
}
