# Контракт: согласование ключа

## `core/dh`

```go
func Group14() *Group                                    // новая копия параметров
func ModPow(b, e, m *big.Int) (*big.Int, error)          // свой, из lab2
func (g *Group) GenerateKey() (*KeyPair, error)          // из crypto/rand
func (k *KeyPair) Public() *big.Int                      // копия
func (g *Group) ValidatePublic(y *big.Int) error         // ErrInvalidPublicValue вне [2, p − 2]
func (k *KeyPair) SharedSecret(peer *big.Int) ([]byte, error) // проверяет peer; ровно 256 байт
```

`KeyPair` не выводит секретное значение через `String` и форматирование. Ошибка источника случайности возвращается вызывающему коду. `GenerateKey` всегда использует `crypto/rand.Reader`; для теста отказа источника есть только неэкспортируемая `generateKey(r io.Reader)`.

## `core/kdf`

```go
func HKDF(ikm, salt, info []byte, length int) ([]byte, error)
func SessionKey(shared []byte, chatID [16]byte, a cipher.Algorithm, keyBits int) ([]byte, error)
```

`HKDF` — RFC 5869 на `crypto/hmac` и `crypto/sha256`. `SessionKey` возвращает `keyBits / 8` байт и не зависит от порядка сторон.

## Сценарий

```text
A: pair := group.GenerateKey()           B: pair := group.GenerateKey()
A → посредник → B: pair.Public()         B → посредник → A: pair.Public()
A: shared := pair.SharedSecret(publicB)  B: shared := pair.SharedSecret(publicA)
Обе стороны: SessionKey(shared, chatID, algorithm, keyBits)
```
