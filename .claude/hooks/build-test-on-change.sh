#!/usr/bin/env bash
# PostToolUse на Write|Edit: быстрая проверка, что Go-модули собираются и тесты зелёные.
# Сервер и клиент могут быть отдельными модулями, поэтому проверяются все go.mod на глубине
# до 2 уровней. Пока их нет (начало работы) — молчим, иначе каждый Write даёт ложную ошибку.
CONTENT=$(cat)  # JSON с tool_input на stdin

# Проект смешанный: правки документов и фронтенда сборку Go затрагивать не могут.
echo "$CONTENT" | grep -qE '"file_path"[[:space:]]*:[[:space:]]*"[^"]*(\.go|go\.mod|go\.sum)"' || exit 0

MODULES=$(find . -maxdepth 3 -name go.mod -not -path './.specify/*' -not -path './.claude/*' 2>/dev/null)
[ -n "$MODULES" ] || exit 0

# Под Git Bash в PATH раньше msys64/ucrt64 стоит Git\mingw64\bin с несовместимыми DLL,
# из-за чего cc1.exe (нужен race-детектору через cgo) падает молча. Ставим ucrt64 первым.
if [ -d /c/msys64/ucrt64/bin ]; then
  export PATH="/c/msys64/ucrt64/bin:$PATH"
fi

# -short: долгие тесты и всё, что требует запущенного брокера, должны пропускаться под
# testing.Short(). Полный прогон — вручную: go test ./...
FAILED=""
for MOD in $MODULES; do
  DIR=$(dirname "$MOD")
  OUT=$(cd "$DIR" && go build ./... 2>&1 && go vet ./... 2>&1 && go test -short ./... 2>&1)
  if [ $? -ne 0 ]; then
    FAILED="$FAILED
[$DIR]
$OUT"
  fi
done

if [ -n "$FAILED" ]; then
  ESCAPED=$(echo "$FAILED" | tail -c 2000 | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))' 2>/dev/null || echo '"build/test failed, see terminal"')
  echo "{\"hookSpecificOutput\":{\"hookEventName\":\"PostToolUse\",\"additionalContext\":\"go build/vet/test FAILED: ${ESCAPED}\"}}"
fi
