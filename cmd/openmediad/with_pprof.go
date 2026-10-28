//go:build omd_pprof

// Компонент pprof (опытный): профилировщик на отладочном порту. Добавить в сборку: go run ./tools/build -with pprof.
package main

import _ "openmediad/internal/debug/pprof"
