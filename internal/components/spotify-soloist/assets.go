package spotifysoloist

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"strings"
)

// Шим libpulse.so.0 собирается из shim/libpulse_shim.c (shim/build.sh, компилятор zig) и лежит в git готовым: бинарник
// openmediad остаётся статическим Go без CGO. Хэш рядом проверяет тест, что файл собран из текущего исходника и не подменён.

//go:embed assets/libpulse.so.0
var shimBlob []byte

//go:embed assets/libpulse.so.0.sha256
var shimSHA256File string

// shimSHA256 — хэш вшитого шима.
func shimSHA256() string {
	sum := sha256.Sum256(shimBlob)
	return hex.EncodeToString(sum[:])
}

// shimHashOK — вшитый шим совпадает с записанным рядом хэшем.
func shimHashOK() bool { return strings.TrimSpace(shimSHA256File) == shimSHA256() }
