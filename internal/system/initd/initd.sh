#!/bin/sh
# S98openmediad — автозапуск openmediad (ставит openmediad install, убирает openmediad uninstall).
# Вся логика — в страже на /data; нет файлов на /data — ничего не делаем: колонка работает штатно.
D=@HOME@
[ -x "$D/bin/openmediad" ] && [ -f "$D/guard.sh" ] || exit 0
case "$1" in
	start | "") sh "$D/guard.sh" boot > /dev/null 2>&1 < /dev/null & ;;
	stop) sh "$D/guard.sh" stop ;;
esac
