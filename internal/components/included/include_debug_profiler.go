//go:build omd_debug_profiler

// Компонент debug-profiler (Профилировщик Go) входит в сборку с тегом omd_debug_profiler: go run ./tools/build -with debug-profiler.
package included

import _ "openmediad/internal/components/debug-profiler"
