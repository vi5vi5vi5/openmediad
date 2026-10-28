//go:build !omd_no_sounds

// Компонент sounds: звонки таймеров и будильников. Убрать из сборки: go run ./tools/build -without sounds.
package main

import _ "openmediad/internal/source/sounds"
