#!/bin/sh
# Страж openmediad. Файл пишет openmediad start (или install) из шаблона в бинарнике — не правьте его: следующий start
# перезапишет. Страж работает на busybox sh отдельно от демона: если демон упал, правило iptables снимается сразу,
# и платформа говорит со штатным mediad напрямую (Алиса не замолкает).
#
#   sh guard.sh boot    при загрузке (скрипт автозапуска): правило, демон, слежение
#   sh guard.sh start   вручную (openmediad start): то же, счётчик падений обнуляется
#   sh guard.sh stop    снять правило, остановить демон (до 15 с) и стража
#
# Правило iptables ставит и снимает только страж. Журнал — @RUN@/guard.log (до 64 КБ).
# при загрузке PATH бывает урезан: системные каталоги — в конец (подделки в тестах остаются первыми)
PATH="$PATH:/usr/sbin:/usr/bin:/sbin:/bin"
export PATH
BIN="@BIN@"
RUN="@RUN@"
STATE="@STATE@"
RULE="-p tcp -d 127.0.0.1 --dport 9906 ! -s 127.0.0.2 -j REDIRECT --to-ports 19906"
LOG="$RUN/guard.log"

log() {
	echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "$LOG"
	if [ "$(wc -c < "$LOG")" -gt 32768 ]; then mv -f "$LOG" "$LOG.1"; fi
}
state() { echo "$*" > "$RUN/guard.state"; }
rule_on() { iptables -t nat -S OUTPUT 2>/dev/null | grep -q -- "--to-ports 19906"; }
# RULE без кавычек намеренно: это несколько аргументов iptables
# shellcheck disable=SC2086
rule_add() { rule_on || iptables -t nat -I OUTPUT 1 $RULE; }
# shellcheck disable=SC2086
rule_del() { while iptables -t nat -D OUTPUT $RULE 2>/dev/null; do :; done; }
# ours PID СЛОВО — процесс жив и в его командной строке есть СЛОВО (pid из файла мог достаться чужому процессу)
ours() { [ -n "$1" ] && tr '\000' ' ' < "/proc/$1/cmdline" 2>/dev/null | grep -q -- "$2"; }

# loop — запускать демон и следить за ним, пока не попросят остановиться или не кончится терпение.
loop() {
	echo $$ > "$RUN/guard.pid"
	trap 'STOP=1; rule_del; [ -n "$DPID" ] && kill -TERM "$DPID" 2>/dev/null' TERM INT
	trap '' HUP
	MAX=$("$BIN" config get fallback.max_crashes 2>/dev/null)
	case "$MAX" in '' | *[!0-9]*) MAX=3 ;; esac
	CRASH=$(cat "$RUN/crashes" 2>/dev/null)
	case "$CRASH" in '' | *[!0-9]*) CRASH=0 ;; esac
	DELAY=2
	# приоритет выше обычного сокращает редкие задержки звука на загруженной колонке; нет nice — запуск как есть
	NICE=
	nice -n -10 true 2> /dev/null && NICE="nice -n -10"
	while [ -z "$STOP" ]; do
		if [ "$CRASH" -ge "$MAX" ]; then
			log "демон упал $CRASH раз(а) подряд — страж сдаётся до перезагрузки или openmediad start; колонка работает штатно"
			state "gave-up $CRASH"
			break
		fi
		if [ -f "$RUN/daemon.err" ] && [ "$(wc -c < "$RUN/daemon.err")" -gt 32768 ]; then
			mv -f "$RUN/daemon.err" "$RUN/daemon.err.1"
		fi
		$NICE "$BIN" run > /dev/null 2>> "$RUN/daemon.err" < /dev/null &
		DPID=$!
		echo "$DPID" > "$RUN/daemon.pid"
		STARTED=$(date +%s)
		rule_add || log "не удалось добавить правило iptables"
		state "running $DPID $STARTED"
		log "демон запущен (pid $DPID), правило iptables стоит"
		# платформа, подключённая к mediad напрямую, сама через шим не пойдёт: reroute ждёт её и при нужде перезапускает
		# mediad (при первом запуске из openmediad start это делает сама команда)
		if [ -n "$REROUTE" ]; then "$BIN" reroute >> "$LOG" 2>&1 < /dev/null & fi
		REROUTE=1
		wait "$DPID"
		CODE=$?
		if [ -n "$STOP" ]; then
			( sleep 15; ours "$DPID" "openmediad run" && kill -KILL "$DPID" ) &
			KILLER=$!
			wait "$DPID"
			kill "$KILLER" 2>/dev/null
			break
		fi
		DPID=
		rule_del
		LIVED=$(($(date +%s) - STARTED))
		if [ "$CODE" -eq 0 ]; then
			log "демон завершился сам (код 0) — правило снято, страж выходит"
			break
		fi
		if [ "$LIVED" -lt 60 ]; then
			CRASH=$((CRASH + 1))
		elif [ "$LIVED" -ge 600 ]; then
			CRASH=0
			DELAY=2
		fi
		echo "$CRASH" > "$RUN/crashes"
		log "демон упал (код $CODE, проработал $LIVED с, быстрых падений подряд $CRASH из $MAX): правило снято, платформа идёт к mediad напрямую"
		[ "$CRASH" -ge "$MAX" ] && continue
		log "повтор через $DELAY с"
		state "restarting $CRASH"
		sleep "$DELAY" &
		wait $!
		DELAY=$((DELAY * 2))
		[ "$DELAY" -gt 64 ] && DELAY=64
	done
	rule_del
	rm -f "$RUN/guard.pid" "$RUN/daemon.pid"
	case "$(cat "$RUN/guard.state" 2>/dev/null)" in gave-up*) ;; *) state "stopped" ;; esac
	log "страж остановлен"
}

mkdir -p "$RUN"
case "$1" in
boot)
	if [ -f "$STATE/autostart-off" ]; then
		log "автозапуск выключен (openmediad enable — включить)"
		state "disabled"
		exit 0
	fi
	[ -x "$BIN" ] || { log "нет $BIN"; exit 1; }
	ours "$(cat "$RUN/guard.pid" 2>/dev/null)" "guard.sh" && { log "страж уже работает"; exit 0; }
	log "загрузка: запуск openmediad"
	REROUTE=1
	loop
	;;
start)
	ours "$(cat "$RUN/guard.pid" 2>/dev/null)" "guard.sh" && { echo "страж уже работает"; exit 1; }
	rm -f "$RUN/crashes"
	log "ручной запуск (openmediad start)"
	REROUTE=
	loop
	;;
stop)
	rule_del
	G=$(cat "$RUN/guard.pid" 2>/dev/null)
	if ours "$G" "guard.sh"; then
		kill -TERM "$G"
		i=0
		while ours "$G" "guard.sh" && [ $i -lt 20 ]; do sleep 1; i=$((i + 1)); done
	fi
	D=$(cat "$RUN/daemon.pid" 2>/dev/null)
	if ours "$D" "openmediad run"; then
		# демон без стража (страж убит?): остановить его самим
		kill -TERM "$D"
		i=0
		while ours "$D" "openmediad run" && [ $i -lt 15 ]; do sleep 1; i=$((i + 1)); done
		ours "$D" "openmediad run" && kill -KILL "$D"
	fi
	rule_del
	rm -f "$RUN/guard.pid" "$RUN/daemon.pid"
	log "остановлено (openmediad stop)"
	state "stopped"
	;;
*)
	echo "использование: sh $0 boot|start|stop" >&2
	exit 2
	;;
esac
