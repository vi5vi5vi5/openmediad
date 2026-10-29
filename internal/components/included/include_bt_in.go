//go:build omd_bt_in

// Компонент bt-in (Bluetooth: звук с телефона через движок) входит в сборку с тегом omd_bt_in: go run ./tools/build -with bt-in.
package included

import _ "openmediad/internal/components/bt-in"
