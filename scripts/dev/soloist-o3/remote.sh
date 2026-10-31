#!/bin/sh
# Выполняется НА КОЛОНКЕ (busybox ash): управление опытным Soloist. Вызывается с компьютера через o3.sh, руками не нужен.
#   remote.sh serve|pair|stop|status|watch [секунд [шаг]]|ctl ...|log [строк]|clean
# Всё лежит в /data/soloist-o3 (флеш), журнал и pid — в /tmp (ОЗУ, пропадут при перезагрузке).
PATH=/usr/bin:/usr/sbin:/bin:/sbin   # шим запускает aplay по имени
D=/data/soloist-o3
LIB=$D/lib
LOG=/tmp/soloist-o3.log
PIDF=/tmp/soloist-o3.pid
NAME=${NAME:-omd-o3}
WS=127.0.0.1:9090
LDR="$LIB/ld-linux-armhf.so.3 --library-path $LIB:/lib"

alive() { [ -f "$PIDF" ] && kill -0 "$(cat "$PIDF")" 2>/dev/null; }
pid() { cat "$PIDF" 2>/dev/null; }
key() { [ -s "$D/api.key" ] || { echo "нет ключа Soloist: ./o3.sh setkey < файл_с_ключом" >&2; exit 1; }; tr -d '\r\n' < "$D/api.key"; }
pss() { awk '/^Pss:/{a+=$2}END{print a+0}' "/proc/$1/smaps" 2>/dev/null; }
memav() { awk '/MemAvailable/{print $2}' /proc/meminfo; }
sz() { du -sk "$1" 2>/dev/null | awk '{print $1}'; }

stop() {
	if alive; then
		p=$(pid)
		kill "$p" 2>/dev/null
		i=0
		while kill -0 "$p" 2>/dev/null && [ $i -lt 10 ]; do sleep 1; i=$((i + 1)); done
		kill -9 "$p" 2>/dev/null
	fi
	rm -f "$PIDF"
	# осиротевшие после kill -9 демона/сеанса: другой экземпляр на том же каталоге завершится с кодом 1
	for q in $(ps | awk '/[s]oloist-o3\/soloist/{print $1}'); do kill -9 "$q" 2>/dev/null; done
}

start() { # start режим-флаги...
	alive && { echo "уже запущен (pid $(pid)); сначала ./o3.sh stop"; exit 0; }
	mkdir -p "$D/data" "$D/cache"
	# без -C Soloist падает: пытается создать /root/.cache на ro-корне. Куда он пишет звуковой кэш при игре (-C или data/cache) — смотрите status.
	$LDR "$D/soloist" -n "$NAME" -k "$(key)" -D "$D/data" -C "$D/cache" -z 100 -i "${VOL:-50}" -w "$WS" -v "$@" > "$LOG" 2>&1 &
	echo $! > "$PIDF"
	sleep 3
}

WDPID=/tmp/soloist-o3-wd.pid
WDLOG=/tmp/soloist-o3-wd.log
wd_alive() { [ -f "$WDPID" ] && kill -0 "$(cat "$WDPID")" 2>/dev/null; }
stop_wd() { wd_alive && kill "$(cat "$WDPID")" 2>/dev/null; rm -f "$WDPID"; }
ticks() { for t in /proc/"$1"/task/*; do echo "${t##*/} $(sed 's/^.*) //' "$t/stat" 2>/dev/null | awk '{print $12+$13}')"; done; }

# Сторож. Проба 31.10.2026: после первого запроса Spotify (zeroconf getInfo) главный поток Soloist примерно через полминуты
# зависает в цикле без системных вызовов (100% одного ядра), перестаёт читать mDNS и принимать HTTP: колонка исчезает из списка.
# Сторож каждые 2 с меряет прирост CPU по потокам и после трёх подряд замеров >= 90% перезапускает Soloist.
wd_loop() {
	bad=0
	while :; do
		if alive; then
			p=$(pid)
			a=$(ticks "$p"); sleep 2; b=$(ticks "$p")
			m=$(printf '%s\n--\n%s\n' "$a" "$b" | awk '/^--$/{s=1;next} !s{x[$1]=$2;next} {d=$2-x[$1]; if(d>m)m=d} END{print m+0}')
			if [ "${m:-0}" -ge 180 ]; then bad=$((bad + 1)); else bad=0; fi
			if [ "$bad" -ge 3 ]; then
				echo "$(date +%T) поток крутится на 100% CPU (прирост $m тиков за 2 с), перезапуск Soloist"
				stop; start; bad=0
			fi
		else
			echo "$(date +%T) Soloist не запущен, запуск"
			start; bad=0
		fi
	done
}

status() {
	echo "== сторож: $(wd_alive && echo работает || echo нет), перезапусков: $(grep -c перезапуск "$WDLOG" 2>/dev/null)"
	echo "== процесс"
	if alive; then
		p=$(pid)
		echo "работает pid=$p PSS=$(pss "$p") КБ потоков=$(ls /proc/"$p"/task 2>/dev/null | wc -l)"
	else
		echo "не запущен"
	fi
	echo "== память и диск"
	echo "MemAvailable=$(memav) КБ (порог doctor 40960 КБ)"
	df -h /data | tail -1
	echo "данные: $(sz "$D/data") КБ (в них data/cache: $(sz "$D/data/cache") КБ), каталог -C: $(sz "$D/cache") КБ, журнал в ОЗУ: $(sz "$LOG") КБ"
	echo "== сокеты (udp 5353 держателей: $(grep -ci ':14E9' /proc/net/udp))"
	grep -E 'shim|websocket server|ZeroConf|mdns|waiting for login|ready|login|session|expires|Access denied|error' "$LOG" 2>/dev/null | cut -c1-200 | tail -12
}

case ${1:-help} in
serve)
	start
	status
	echo
	echo "Дальше: в приложении Spotify (телефон/ПК в этой же сети) выберите устройство \"$NAME\"; ./o3.sh status и ./o3.sh log покажут вход."
	;;
watchdog) # запустить сторожа в фоне на колонке (остановка: stop)
	stop_wd
	nohup sh "$0" wd-loop </dev/null >> "$WDLOG" 2>&1 &
	echo $! > "$WDPID"
	echo "сторож запущен (pid $!), журнал: $WDLOG"
	;;
wd-loop) wd_loop ;;
pair)
	stop_wd
	stop
	start -p
	echo "Режим сопряжения (-p): выберите \"$NAME\" в приложении Spotify. Жду до 3 минут..."
	i=0
	while alive && [ $i -lt 90 ]; do sleep 2; i=$((i + 1)); done
	grep -E 'login|session|Access denied|auth|pair|error|exit' "$LOG" | cut -c1-220 | tail -15
	alive && echo "(процесс ещё работает)" || echo "(процесс завершился: сопряжение сохранено или ошибка выше)"
	;;
stop) stop_wd; stop; echo "остановлен" ;;
status) status ;;
log) tail -n "${2:-60}" "$LOG" | cut -c1-240 ;;
watch)
	N=${2:-60}
	S=${3:-5}
	echo "время_с,MemAvailable_КБ,PSS_КБ,потоков,каталог_C_КБ,data_КБ,load1"
	i=0
	while [ $i -lt "$N" ]; do
		if alive; then p=$(pid); a=$(pss "$p"); t=$(ls /proc/"$p"/task 2>/dev/null | wc -l); else a=0; t=0; fi
		echo "$((i * S)),$(memav),$a,$t,$(sz "$D/cache"),$(sz "$D/data"),$(cut -d' ' -f1 /proc/loadavg)"
		i=$((i + 1))
		sleep "$S"
	done
	;;
trace) # поток всех событий WebSocket (вход, playback_state, position_sync, ошибки) на N секунд; копия в /tmp/soloist-o3.trace
	S=${2:-60}
	T=/tmp/soloist-o3.trace
	: > "$T"
	$LDR "$D/soloist" ctl trace -w "$WS" > "$T" 2>&1 &
	tp=$!
	n=0
	i=0
	while [ $i -lt "$S" ]; do
		sleep 1
		i=$((i + 1))
		c=$(wc -l < "$T")
		if [ "$c" -gt "$n" ]; then tail -n +$((n + 1)) "$T" | grep -v 'no version information'; n=$c; fi
	done
	kill "$tp" 2>/dev/null
	;;
tone) shift; $LDR "$D/shim_test" "${1:-4}" "${2:-0.15}" ;;   # тот же загрузчик glibc 2.41, что у Soloist
ctl) shift; $LDR "$D/soloist" ctl "$@" -w "$WS" ;;   # ctl по умолчанию ищет другой data-dir, поэтому адрес WS задаём явно
clean)
	stop_wd
	stop
	rm -rf "$D" "$LOG" /tmp/soloist-o3.trace "$PIDF" "$WDLOG"
	echo "удалено: $D, $LOG"
	;;
*)
	echo "serve | watchdog | pair | stop | status | log [N] | watch [N [шаг]] | trace [сек] | tone [сек [ампл]] | ctl ... | clean"
	;;
esac
