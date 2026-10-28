//go:build omd_bluetooth

// Компонент bluetooth (опытный, пока не проверен с наушниками — PLAN.md, R8): выход в Bluetooth-наушники. Добавить в сборку:
// go run ./tools/build -with bluetooth.
package main

import _ "openmediad/internal/sink/bluetooth"
