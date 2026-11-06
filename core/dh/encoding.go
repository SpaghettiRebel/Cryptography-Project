package dh

import "math/big"

// PublicValueSize — длина открытого значения при передаче: 2048 бит группы 14.
const PublicValueSize = 256

// PublicBytes возвращает открытое значение: беззнаковое big-endian, ровно 256 байт, с ведущими нулями.
func (k *KeyPair) PublicBytes() []byte {
	return k.y.FillBytes(make([]byte, PublicValueSize))
}

// ParsePublic разбирает значение собеседника и проверяет его длину и диапазон.
func (g *Group) ParsePublic(raw []byte) (*big.Int, error) {
	if len(raw) != PublicValueSize {
		return nil, ErrInvalidPublicValue
	}
	y := new(big.Int).SetBytes(raw)
	if err := g.ValidatePublic(y); err != nil {
		return nil, err
	}
	return y, nil
}
