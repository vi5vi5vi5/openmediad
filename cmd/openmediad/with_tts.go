//go:build !omd_no_tts

// Компонент tts: голос Алисы. Убрать из сборки: go run ./tools/build -without tts.
package main

import _ "openmediad/internal/source/tts"
