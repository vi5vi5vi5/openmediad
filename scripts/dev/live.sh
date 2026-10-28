#!/bin/sh
# Живая запись: наблюдатель openmediad-dev на колонке + независимый дамп трафика шины (tcpdump) для сверки.
# Результаты — в captures/ (в .gitignore: там могут быть идентификаторы аккаунта).
#
#   scripts/live.sh имя секунды [хост] [ssh-порт]
#
# Потом: go run ./cmd/openmediad-dev pcap -progress captures/имя.pcap
DEV=${DEV:-/tmp/openmediad/openmediad-dev}   # openmediad-dev на колонке: go run ./tools/build -dev -deploy адрес
set -eu

NAME=${1:?имя записи}
SECS=${2:-120}
HOST=${3:-192.168.1.12}
PORT=${4:-2222}
cd "$(dirname "$0")/../.."
mkdir -p captures

ssh -p "$PORT" "root@$HOST" "$DEV observe -progress -stats 10s & P=\$!; sleep $SECS; kill \$P" \
  < /dev/null > "captures/$NAME.observe.log" 2>&1 &
A=$!
ssh -p "$PORT" "root@$HOST" "/opt/bin/tcpdump -i lo -s 0 -U -w - 'tcp and not port 8765' 2>/dev/null & P=\$!; sleep $SECS; kill \$P" \
  < /dev/null > "captures/$NAME.pcap" &
B=$!
wait $A $B
ls -l "captures/$NAME".*
