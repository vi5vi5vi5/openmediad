//go:build omd_btin

// Компонент btin (опытный): звук с телефона по Bluetooth идёт через движок openmediad, а не штатный bluealsa-aplay.
// Добавить в сборку: go run ./tools/build -with bluetooth,btin.
package main

import _ "openmediad/internal/source/btin"
