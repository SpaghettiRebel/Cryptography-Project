//go:build devcipher

package message

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"

	"cryptochat/core/algorithms"
	"cryptochat/core/cipher"
	"cryptochat/core/modes"
	"cryptochat/core/protocol"
)

// Полный цикл через публичный API, без внутренних точек подмены.
func TestPublicAPI(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	p := Params{
		Settings: cipher.Settings{Algorithm: algorithms.DevAlgorithm, KeyBits: 256, Mode: modes.CBC, Padding: modes.PKCS7},
		Key:      key,
		ChatID:   [protocol.IDSize]byte{1},
	}
	data := make([]byte, ChunkSize+7)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	meta := Meta{Name: "файл", Type: "тип", TotalSize: int64(len(data))}
	id := [protocol.IDSize]byte{2}
	out := &memWriter{}
	a, err := NewAssembler(p, id, out, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = Seal(context.Background(), p, id, meta, bytes.NewReader(data),
		func(f *protocol.Fragment) error { return a.Add(context.Background(), f) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Finish()
	if err != nil || got != meta || !bytes.Equal(out.data, data) {
		t.Fatalf("результат: %+v, %v", got, err)
	}
}
