//go:build !omd_no_spectrum

// Компонент spectrum: спектр для дисплея и подсветки (iohub). Убрать из сборки: go run ./tools/build -without spectrum.
package main

import _ "openmediad/internal/quasar/iohub"
