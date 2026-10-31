#!/bin/sh
# Опыт О1/О3 без шима звука: Soloist (arm32) на колонке с подложенным glibc 2.41. Запускать на компьютере (Git Bash).
# Что проверяет: берёт ли Soloist ваш аккаунт (вход/сопряжение), как ведёт себя воспроизведение без аудиовыхода,
# память, рост кэша. Звука здесь нет: нужен шим libpulse (см. README.md).
#
#   scripts/dev/soloist-o3/o3.sh deploy            скачать Soloist и glibc на компьютер, залить на колонку в /data/soloist-o3
#   scripts/dev/soloist-o3/o3.sh setkey < файл     записать ключ Soloist (из Developer Dashboard) на колонку, права 0600
#   scripts/dev/soloist-o3/o3.sh tone [сек [амплитуда]]   проверка шима тоном (без Spotify): должны услышать «ля» на колонке
#   scripts/dev/soloist-o3/o3.sh build-shim        собрать шим (нужен zig: ZIG=/путь/к/zig)
#   scripts/dev/soloist-o3/o3.sh serve|watchdog|pair|stop|status|log|watch|trace|ctl ...   см. remote.sh
#   watchdog = serve + сторож, перезапускающий Soloist при зависании на 100% CPU (см. README, «Известные проблемы»)
#   scripts/dev/soloist-o3/o3.sh clean             остановить и стереть /data/soloist-o3 (ключ, сеанс, кэш)
#
# Адрес колонки: HOST (по умолчанию 192.168.1.12) и PORT (2222), как в остальных скриптах.
set -eu

HOST=${HOST:-192.168.1.12}
PORT=${PORT:-2222}
SSH="ssh -p $PORT -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR root@$HOST"
here=$(cd "$(dirname "$0")" && pwd)
cd "$here/../../.."
CACHE=captures/soloist-o3   # captures/ в .gitignore: сборки Soloist распространять нельзя
R=/data/soloist-o3

SOLOIST_URL=https://soloist-builds.spotifycdn.com/soloist_release_arm32.tar.gz
LIBC_URL=${LIBC_URL:-http://deb.debian.org/debian/pool/main/g/glibc/libc6_2.41-12+deb13u4_armhf.deb}
LIBC_SHA256=${LIBC_SHA256:-4fc6fed8d77d01c0dacb5fda8b880a8c8a669575936de5761b693077a6f6c2ec}
LIBS="ld-linux-armhf.so.3 libc.so.6 libm.so.6 libpthread.so.0 libdl.so.2 librt.so.1 libresolv.so.2 libnss_dns.so.2 libnss_files.so.2"

py() { if command -v python >/dev/null 2>&1; then PYTHONIOENCODING=utf-8 python -I "$@"; else PYTHONIOENCODING=utf-8 python3 -I "$@"; fi; }

push() { # локальный_файл удалённый_путь режим
	want=$(sha256sum "$1" | cut -d' ' -f1)
	have=$($SSH "sha256sum '$2' 2>/dev/null" </dev/null | cut -d' ' -f1 || true)
	if [ "$want" = "$have" ]; then echo "= $2 (уже есть)"; return; fi
	$SSH "mkdir -p '$(dirname "$2")' && cat > '$2.new' && chmod $3 '$2.new' && mv '$2.new' '$2'" < "$1"
	echo "+ $2"
}

deploy() {
	mkdir -p "$CACHE/x" "$CACHE/lib"
	if [ ! -s "$CACHE/soloist_release_arm32.tar.gz" ]; then
		echo "скачиваю Soloist arm32 с CDN Spotify (~12,5 МБ; распространять нельзя, остаётся только у вас)"
		curl -fL --max-time 300 -o "$CACHE/soloist_release_arm32.tar.gz" "$SOLOIST_URL"
	fi
	tar -xzf "$CACHE/soloist_release_arm32.tar.gz" -C "$CACHE/x"
	if [ ! -s "$CACHE/libc6.deb" ]; then
		echo "скачиваю glibc 2.41 armhf (Debian, ~2,3 МБ)"
		curl -fL --max-time 120 -o "$CACHE/libc6.deb" "$LIBC_URL"
	fi
	got=$(sha256sum "$CACHE/libc6.deb" | cut -d' ' -f1)
	if [ "$got" != "$LIBC_SHA256" ]; then
		echo "sha256 libc6.deb не совпал: $got" >&2
		echo "Если Debian обновил пакет, проверьте его и задайте LIBC_URL и LIBC_SHA256." >&2
		exit 1
	fi
	# shellcheck disable=SC2086
	py "$here/debx.py" "$CACHE/libc6.deb" "$CACHE/lib" $LIBS
	$SSH "mkdir -p $R/lib $R/data" </dev/null
	push "$CACHE/x/soloist" "$R/soloist" 755
	for f in $LIBS; do push "$CACHE/lib/$f" "$R/lib/$f" 755; done
	if [ -s "$CACHE/libpulse.so.0" ]; then
		push "$CACHE/libpulse.so.0" "$R/lib/libpulse.so.0" 755
		push "$CACHE/shim_test" "$R/shim_test" 755
	else
		echo "шима libpulse нет (captures/soloist-o3/libpulse.so.0): Soloist будет без звука. Сборка: ZIG=/путь/к/zig sh $0 build-shim"
	fi
	push "$here/remote.sh" "$R/remote.sh" 755
	echo
	$SSH "$R/lib/ld-linux-armhf.so.3 --library-path $R/lib:/lib $R/soloist -V 2>&1 | tail -1; df -h /data | tail -1" </dev/null
	echo "Готово. Дальше: ключ (setkey), затем serve и выбор устройства omd-o3 в приложении Spotify."
}

# настройки шима и запуска пробрасываем на колонку (ssh сам окружение не передаёт)
pre=""
for v in NAME VOL OMD_SHIM_DEBUG OMD_SHIM_VOLUME OMD_SHIM_LAT_US OMD_SHIM_NO_UNDERFLOW OMD_PCM_DEV OMD_PCM_APLAY_ARGS OMD_PCM_FIFO OMD_PCM_CMD OMD_PCM_PIPE_BYTES; do
	eval "val=\${$v:-}"
	[ -n "$val" ] && pre="$pre $v='$val'"
done

case ${1:-help} in
deploy) deploy ;;
build-shim) sh "$here/shim/build.sh" ;;
setkey)
	[ -t 0 ] && { echo "ключ читается со stdin: o3.sh setkey < файл_с_ключом" >&2; exit 1; }
	$SSH "umask 077; mkdir -p $R && cat > $R/api.key && chmod 600 $R/api.key && wc -c < $R/api.key"
	echo "ключ записан на колонку ($R/api.key, 0600). В argv процесса Soloist он будет виден локально (особенность Soloist)."
	;;
serve | watchdog | pair | stop | status | log | watch | trace | tone | ctl | clean)
	$SSH "$pre sh $R/remote.sh $*" </dev/null
	;;
*)
	sed -n '2,12p' "$0"
	;;
esac
