#!/bin/sh
# Сборка и тесты интерфейса в контейнере node:22-alpine; Node на машине не нужен.
# Использование: sh client/scripts/frontend.sh [build|test]
#   без аргумента — npm ci (или npm install, если нет package-lock.json), сборка и тесты;
#   build — только сборка, test — только тесты (зависимости ставятся, если тома ещё нет).
# node_modules хранится в томе Docker cryptochat-client-node-modules, а не в папке проекта.
set -u

DIR=$(cd "$(dirname "$0")/../frontend" && (pwd -W 2>/dev/null || pwd))
if [ ! -f "$DIR/package-lock.json" ]; then
	INSTALL="npm install"
else
	INSTALL="npm ci"
fi

case "${1:-all}" in
all) CMD="$INSTALL && npm run build && npm test" ;;
build) CMD="([ -x node_modules/.bin/vite ] || $INSTALL) && npm run build" ;;
test) CMD="([ -x node_modules/.bin/vitest ] || $INSTALL) && npm test" ;;
*)
	echo "использование: $0 [build|test]" >&2
	exit 2
	;;
esac

MSYS_NO_PATHCONV=1 docker run --rm \
	-v "$DIR:/app" \
	-v cryptochat-client-node-modules:/app/node_modules \
	-w /app \
	node:22-alpine sh -c "$CMD"
