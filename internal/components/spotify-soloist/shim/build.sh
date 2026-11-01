#!/bin/sh
# Сборка шима libpulse.so.0 (и теста shim_test) для колонки: ARM hard-float, glibc >= 2.30, компилятор zig.
#   ZIG=/путь/к/zig sh internal/components/spotify-soloist/shim/build.sh
# Результат:
#   internal/components/spotify-soloist/assets/libpulse.so.0   готовый шим (вшивается в бинарник openmediad, лежит в git);
#   internal/components/spotify-soloist/assets/libpulse.so.0.sha256   его хэш (проверяет тест компонента);
#   captures/soloist-o3/shim_test                              тест для колонки (captures/ в .gitignore).
# zig: официальный 0.17.0 с https://ziglang.org/download/ (Windows x86_64: sha256 b5663f69581dcf391293fbf16c06cb80d81d806545ce618b4d0bab7f0eb8c428).
# Заголовки PulseAudio берутся из пакета libpulse-dev (Debian trixie, armhf): компилятор сам проверяет сигнатуры 48 функций.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
cd "$here/../../../.."
CACHE=captures/soloist-o3
ASSETS=internal/components/spotify-soloist/assets
ZIG=${ZIG:-zig}
if ! "$ZIG" version >/dev/null 2>&1; then
	echo "нет компилятора zig: задайте ZIG=/путь/к/zig (см. комментарий в начале скрипта)" >&2
	exit 1
fi
PA_URL=${PA_URL:-http://deb.debian.org/debian/pool/main/p/pulseaudio/libpulse-dev_17.0+dfsg1-3_armhf.deb}
PA_SHA256=${PA_SHA256:-7e427c2f6b0049314b388213e229bccddd799ac53e45f997a41da91e965afb93}
mkdir -p "$CACHE" "$ASSETS"
if [ ! -d "$CACHE/pa-include/pulse" ]; then
	curl -fL --max-time 120 -o "$CACHE/libpulse-dev.deb" "$PA_URL"
	if [ -n "$PA_SHA256" ] && [ "$(sha256sum "$CACHE/libpulse-dev.deb" | cut -d' ' -f1)" != "$PA_SHA256" ]; then
		echo "sha256 libpulse-dev.deb не совпал" >&2
		exit 1
	fi
	if command -v python >/dev/null 2>&1; then PY=python; else PY=python3; fi
	PYTHONIOENCODING=utf-8 $PY -I "$here/../../../../scripts/dev/soloist-o3/debx.py" "$CACHE/libpulse-dev.deb" "$CACHE/pa-include" --include
fi
T="-target arm-linux-gnueabihf.2.30 -mcpu=cortex_a9"
# shellcheck disable=SC2086
"$ZIG" cc $T -Os -fPIC -shared -Wall -Wextra -Wno-unused-parameter -I "$CACHE/pa-include" -Wl,-soname,libpulse.so.0 -Wl,--strip-all \
	-o "$ASSETS/libpulse.so.0" "$here/libpulse_shim.c" -lpthread -lm
(cd "$ASSETS" && sha256sum libpulse.so.0 | cut -d' ' -f1 > libpulse.so.0.sha256)
# shellcheck disable=SC2086
"$ZIG" cc $T -O2 -Wall -Wno-unused-parameter -I "$CACHE/pa-include" -o "$CACHE/shim_test" "$here/shim_test.c" \
	"$ASSETS/libpulse.so.0" -Wl,-rpath,'$ORIGIN' -ldl -lm -lpthread
ls -l "$ASSETS/libpulse.so.0" "$CACHE/shim_test"
cat "$ASSETS/libpulse.so.0.sha256"
