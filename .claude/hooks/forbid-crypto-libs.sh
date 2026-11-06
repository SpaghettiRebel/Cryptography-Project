#!/usr/bin/env bash
# PreToolUse на Write|Edit. Два уровня строгости:
#   deny — однозначно запрещено заданием;
#   ask  — конструкции, допустимые в одном контексте и запрещённые в другом, — решает человек.
#
# В отличие от лабораторных, здесь хеши разрешены: задание прямо требует хешировать пароли и
# запрещает сторонние реализации только симметричных алгоритмов и протокола Диффи-Хеллмана.
# Покрыты Go и JS/TS. Если в плане выбран другой стек, добавь его библиотеки сюда.
CONTENT=$(cat)  # JSON с tool_input на stdin

deny() {
  printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"%s"}}\n' "$1"
  exit 0
}
ask() {
  printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"%s"}}\n' "$1"
  exit 0
}

# Кавычки ищем как \\?" — содержимое файла приходит хуку внутри JSON, там они экранированы.
# Требование кавычек с обеих сторон отсекает упоминания в документации (`crypto/aes`).

# 1. Разработка на Python не допускается.
if echo "$CONTENT" | grep -qE '"file_path"[[:space:]]*:[[:space:]]*"[^"]*\.py"'; then
  deny "Разработка на Python не допускается заданием курсового проекта."
fi

# 2. Go: готовые шифры, режимы и обмен ключами из stdlib.
if echo "$CONTENT" | grep -qE '\\?"crypto/(aes|des|rc4|cipher|ecdh|mlkem)\\?"'; then
  deny "Запрещённый пакет: сторонняя реализация шифра, режима или обмена ключами. Симметричные алгоритмы, режимы и Диффи-Хеллман реализуются вручную. Для паролей хеши разрешены."
fi

# 3. Go: golang.org/x/crypto — всё, кроме функций хеширования и выработки ключа из пароля.
if echo "$CONTENT" | grep -oE '\\?"golang\.org/x/crypto/[a-z0-9]+' | grep -qvE '/(bcrypt|argon2|scrypt|pbkdf2|sha3|blake2b|blake2s)$'; then
  deny "Из golang.org/x/crypto разрешены только bcrypt, argon2, scrypt, pbkdf2, sha3, blake2. Шифры и обмен ключами реализуются вручную."
fi

# 4. Любой язык: сторонние библиотеки алгоритмов из приложения 1 и протокола Диффи-Хеллмана.
if echo "$CONTENT" | grep -qiE '\\?"[^"\\ ]*(camellia|loki97|macguffin|magenta-cipher|mars-cipher|rc5|rc6|serpent|twofish|diffie-?hellman)[^"\\ ]*\\?"[[:space:]]*(\)|;|,|$|\\n)'; then
  ask "Похоже на подключение сторонней реализации алгоритма из приложения 1 или протокола Диффи-Хеллмана: это запрещено заданием. Подтверди, только если это не импорт, а обычная строка, например имя алгоритма в API."
fi

# 5. JS/TS: готовая криптография для клиента.
if echo "$CONTENT" | grep -qE "[\"'](crypto-js|node-forge|libsodium-wrappers|tweetnacl|sjcl)[\\\\\"']|createCipheriv|createDecipheriv|createDiffieHellman|crypto\.subtle\.(encrypt|decrypt|deriveKey|deriveBits)"; then
  deny "Готовая криптография в JS: шифрование и Диффи-Хеллман реализуются вручную."
fi

# 6. Exp с модулем — не протокол, но решать должен человек: собственный ModPow есть в lab2.
if echo "$CONTENT" | grep -qE '\.Exp\([^)]*,[^)]*,[^)]*\)'; then
  ask "big.Int.Exp с модулем: готовое возведение в степень для Диффи-Хеллмана. Задание запрещает сторонние реализации протокола; безопаснее взять свой ModPow. Подтверди, только если это осознанное решение."
fi

# 7. math/rand — предсказуемый генератор: недопустим для секретов DH, ключей и IV.
if echo "$CONTENT" | grep -qE '\\?"math/rand(/v2)?\\?"|Math\.random\('; then
  ask "Предсказуемый генератор случайных чисел. Секреты Диффи-Хеллмана, ключи и IV обязаны браться из криптостойкого источника. Подтверди, только если случайность здесь не криптографическая."
fi

printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}\n'
