#!/bin/sh
# Сборка приложения: привязки Wails, интерфейс в контейнере, окно.
# Использование: sh client/scripts/build.sh [аргументы wails build], например -tags devcipher.
# Нужны Go, Docker и Wails CLI v2.9.2 (путь к нему можно задать переменной WAILS).
# Результат: client/build/bin/cryptochat.exe.
set -eu

CLIENT=$(cd "$(dirname "$0")/.." && pwd)
WAILS=${WAILS:-wails}

cd "$CLIENT"
echo "== привязки: wails generate module"
"$WAILS" generate module
echo "== интерфейс: scripts/frontend.sh"
sh "$CLIENT/scripts/frontend.sh"
echo "== приложение: wails build -s $*"
"$WAILS" build -s "$@"
