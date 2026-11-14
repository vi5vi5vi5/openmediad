package proto

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Эталон получен тем же WebCrypto и с теми же параметрами, что в веб-клиенте MeetUp (Server/web/assets/meetup-media.js:
// deriveKeyFromPhrase и makeCipher), запуском в Node. Совпадение здесь означает, что колонку и браузер разделяет только сеть.
const (
	refPhrase = "секретная фраза"
	refRoom   = "AbCdEfGhIjKlMnOpQrStUvWx"
	refKeyHex = "d6fbe43878d322b35874da899f88baa51f0191a8219c02e0a9186657ba004295"
	refKeyB64 = "1vvkOHjTIrNYdNqJn4i6pR8BkaghnALgqRhmV7oAQpU"
	// чат «!next привет»: префикс IV 01 02 03 04, счётчик 5
	refChat = "🔒e2e:AQIDBAUAAAAAAAAAmna3NDiipKYVOBjXBhvMokPJyyQhE365vJtOAFEWjwUFRQ"
	// звуковой кадр F8 01 02 03 (тип 4, кодек 4): префикс IV AA BB CC DD, счётчик 0
	refMediaHex = "aabbccdd0000000000000000bef46a2fca50c6b64fd18f1a6b4454b19f811426"
)

func TestCipherMatchesWebClient(t *testing.T) {
	key := KeyFromPhrase(refPhrase, refRoom)
	if hex.EncodeToString(key) != refKeyHex {
		t.Fatalf("ключ из фразы: %x", key)
	}
	if k, err := KeyFromLink(refKeyB64); err != nil || !bytes.Equal(k, key) {
		t.Fatalf("ключ из ссылки: %x, %v", k, err)
	}
	if bytes.Equal(KeyFromPhrase(refPhrase, "другая комната"), key) {
		t.Fatal("одна фраза дала один ключ в разных комнатах")
	}
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}

	// расшифровка того, что зашифровал браузер
	if text, ok := c.OpenChat(refChat); !ok || text != "!next привет" {
		t.Fatalf("чат браузера: %q, %v", text, ok)
	}
	media, _ := hex.DecodeString(refMediaHex)
	if plain, ok := c.Open(msgAudioCoded, codecOpus, media); !ok || !bytes.Equal(plain, []byte{0xF8, 1, 2, 3}) {
		t.Fatalf("кадр браузера: %x, %v", plain, ok)
	}

	// шифрование: с теми же IV получается байт в байт то же, что у браузера
	c.prefix = [4]byte{1, 2, 3, 4}
	c.counter.Store(5)
	if got := c.SealChat("!next привет"); got != refChat {
		t.Fatalf("наш чат:\n %s\nэталон:\n %s", got, refChat)
	}
	c.prefix = [4]byte{0xAA, 0xBB, 0xCC, 0xDD}
	c.counter.Store(0)
	if got := c.Seal(msgAudioCoded, codecOpus, []byte{0xF8, 1, 2, 3}); !bytes.Equal(got, media) {
		t.Fatalf("наш кадр: %x", got)
	}
}

func TestCipherRejectsWrongKeyAndTampering(t *testing.T) {
	a, _ := NewCipher(KeyFromPhrase("одна", refRoom))
	b, _ := NewCipher(KeyFromPhrase("другая", refRoom))
	sealed := a.Seal(msgAudioCoded, codecOpus, []byte("звук"))
	if _, ok := b.Open(msgAudioCoded, codecOpus, sealed); ok {
		t.Fatal("чужой ключ открыл кадр")
	}
	if _, ok := a.Open(msgAudioCoded, codecOpus+1, sealed); ok {
		t.Fatal("кадр открылся с подменённым кодеком")
	}
	sealed[len(sealed)-1] ^= 1
	if _, ok := a.Open(msgAudioCoded, codecOpus, sealed); ok {
		t.Fatal("испорченный кадр открылся")
	}
	for _, bad := range []string{"привет", ChatCipherPrefix, ChatCipherPrefix + "!!!", ChatCipherPrefix + "AAAA"} {
		if _, ok := a.OpenChat(bad); ok {
			t.Fatalf("%q принято за шифровку", bad)
		}
	}
	// IV не повторяется: счётчик растёт, префикс у каждой сессии свой
	x, y := a.Seal(1, 1, nil), a.Seal(1, 1, nil)
	if bytes.Equal(x[:ivSize], y[:ivSize]) {
		t.Fatal("IV повторился")
	}
	if bytes.Equal(a.prefix[:], b.prefix[:]) {
		t.Fatal("у двух шифров один префикс IV")
	}
	for _, bad := range []string{"", "коротко", refKeyB64[:20], refKeyB64 + "AAAA"} {
		if _, err := KeyFromLink(bad); err == nil {
			t.Fatalf("ключ %q принят", bad)
		}
	}
	if _, err := KeyFromLink(refKeyB64 + "="); err != nil {
		t.Fatalf("ключ с дополнением отвергнут: %v", err)
	}
	if _, err := NewCipher([]byte("короткий")); err == nil {
		t.Fatal("короткий ключ принят")
	}
}
