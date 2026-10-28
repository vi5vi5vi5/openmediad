//go:build !omd_no_yandex

// Компонент yandex: музыка Яндекса. Убрать из сборки: go run ./tools/build -without yandex.
package main

import _ "openmediad/internal/source/yandex"
