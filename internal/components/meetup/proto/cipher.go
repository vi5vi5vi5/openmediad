package proto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"sync/atomic"
)

// Сквозное шифрование MeetUp (§7): AES-256-GCM, ключ есть только у участников, сервер пересылает шифротекст.
//
// Шифрованные данные: [IV:12][шифротекст][метка GCM:16]. IV = [случайный префикс сессии:4][счётчик:8 LE] — не повторяется в
// пределах ключа. AAD — два байта [тип, кодек] исходного сообщения: подмена типа по пути ломает проверку.

const (
	keySize        = 32
	ivSize         = 12
	tagSize        = 16
	kdfIterations  = 150000
	kdfSaltPrefix  = "meetup-e2e-v1|"
	chatAADType    = 250 // «тип» для текста чата (§7.3)
	flagEncrypted  = 2   // флаг кадра: данные зашифрованы (§5.2)
	maxCipherInput = 1 << 20
)

// KeyFromLink разбирает ключ из ссылки-приглашения (часть после «#k=»): 32 байта в base64url без дополнения.
func KeyFromLink(b64 string) ([]byte, error) {
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(b64, "="))
	if err != nil || len(key) != keySize {
		return nil, errors.New("ключ шифрования в ссылке испорчен: скопируйте ссылку-приглашение целиком")
	}
	return key, nil
}

// KeyFromPhrase выводит ключ из фразы, которую вводят все участники: PBKDF2-HMAC-SHA256, 150 000 итераций, соль включает код
// комнаты (одна фраза в разных комнатах даёт разные ключи). На Станции Мини 3 это доли секунды: звать не под замком.
func KeyFromPhrase(phrase, room string) []byte {
	key, err := pbkdf2.Key(sha256.New, phrase, []byte(kdfSaltPrefix+room), kdfIterations, keySize)
	if err != nil { // параметры постоянные: ошибки быть не может
		panic(err)
	}
	return key
}

// Cipher шифрует и расшифровывает данные одной сессии отправки. Годится для нескольких горутин.
type Cipher struct {
	aead    cipher.AEAD
	prefix  [4]byte
	counter atomic.Uint64
}

// NewCipher создаёт шифр с ключом key (32 байта) и новым случайным префиксом IV.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != keySize {
		return nil, errors.New("ключ шифрования должен быть длиной 32 байта")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	c := &Cipher{aead: aead}
	if _, err := rand.Read(c.prefix[:]); err != nil {
		return nil, err
	}
	return c, nil
}

// Seal шифрует данные сообщения типа typ с кодеком codec.
func (c *Cipher) Seal(typ, codec byte, plain []byte) []byte {
	out := make([]byte, ivSize, ivSize+len(plain)+tagSize)
	copy(out, c.prefix[:])
	binary.LittleEndian.PutUint64(out[4:], c.counter.Add(1)-1)
	return c.aead.Seal(out, out[:ivSize], plain, []byte{typ, codec})
}

// Open расшифровывает данные; ok=false — ключ не тот или данные испорчены.
func (c *Cipher) Open(typ, codec byte, data []byte) (plain []byte, ok bool) {
	if len(data) < ivSize+tagSize || len(data) > maxCipherInput {
		return nil, false
	}
	plain, err := c.aead.Open(nil, data[:ivSize], data[ivSize:], []byte{typ, codec})
	return plain, err == nil
}

// SealChat шифрует текст сообщения чата: «🔒e2e:» и base64url без дополнения (§7.3).
func (c *Cipher) SealChat(text string) string {
	return ChatCipherPrefix + base64.RawURLEncoding.EncodeToString(c.Seal(chatAADType, 0, []byte(text)))
}

// OpenChat расшифровывает сообщение чата, начинающееся с «🔒e2e:»; ok=false — не шифровка, ключ не тот или данные испорчены.
func (c *Cipher) OpenChat(s string) (text string, ok bool) {
	b64, found := strings.CutPrefix(s, ChatCipherPrefix)
	if !found {
		return "", false
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(b64, "="))
	if err != nil {
		return "", false
	}
	plain, ok := c.Open(chatAADType, 0, data)
	return string(plain), ok
}
