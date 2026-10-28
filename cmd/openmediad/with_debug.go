//go:build !omd_no_debug

// Компонент debug: отладочный веб-порт. Убрать из сборки: go run ./tools/build -without debug.
package main

import _ "openmediad/internal/debug"
