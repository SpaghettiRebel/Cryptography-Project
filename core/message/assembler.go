package message

import (
	"context"
	"fmt"
	"io"
	"sync"

	"cryptochat/core/cipher"
	"cryptochat/core/cryptoctx"
	"cryptochat/core/padding"
	"cryptochat/core/protocol"
)

// Assembler собирает сообщение из фрагментов в любом порядке. Add безопасен при одновременных вызовах;
// каждый вызов держит в памяти до трёх объёмов фрагмента, поэтому число одновременных вызовов
// ограничивает вызывающий код.
type Assembler struct {
	params    Params
	messageID [protocol.IDSize]byte
	cipher    cipher.BlockCipher
	out       io.WriterAt
	progress  Progress

	mu        sync.Mutex
	total     uint32
	totalSize int64
	claimed   []uint64 // номера, запись которых начата или завершена
	written   uint32
	done      int64
	meta      Meta
	haveMeta  bool
}

// NewAssembler создаёт сборку сообщения; данные записываются в w по смещению index · ChunkSize.
func NewAssembler(p Params, messageID [protocol.IDSize]byte, w io.WriterAt, progress Progress) (*Assembler, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	c, err := p.keyedCipher()
	if err != nil {
		return nil, err
	}
	return newAssembler(c, p, messageID, w, progress)
}

func newAssembler(c cipher.BlockCipher, p Params, messageID [protocol.IDSize]byte, w io.WriterAt, progress Progress) (*Assembler, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &Assembler{params: p, messageID: messageID, cipher: c, out: w, progress: progress}, nil
}

func bit(set []uint64, i uint32) bool { return set[i/64]&(1<<(i%64)) != 0 }

// Add расшифровывает и записывает фрагмент. Повтор уже принятого номера игнорируется без сверки
// содержимого: остаётся первый фрагмент, аутентификации нет.
func (a *Assembler) Add(ctx context.Context, f *protocol.Fragment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f == nil || f.ChatID != a.params.ChatID || f.MessageID != a.messageID {
		return ErrFragmentMismatch
	}
	if f.Total < 1 || f.Total > protocol.MaxTotal || f.Index >= f.Total {
		return ErrFragmentMismatch
	}

	a.mu.Lock()
	if a.total != 0 {
		if f.Total != a.total {
			a.mu.Unlock()
			return ErrFragmentMismatch
		}
		if bit(a.claimed, f.Index) {
			a.mu.Unlock()
			return nil
		}
	}
	a.mu.Unlock()

	in, err := a.decrypt(ctx, f)
	if err != nil {
		return err
	}

	a.mu.Lock()
	if a.total == 0 {
		a.total, a.totalSize = f.Total, in.totalSize
		a.claimed = make([]uint64, (f.Total+63)/64)
	} else if f.Total != a.total || in.totalSize != a.totalSize {
		a.mu.Unlock()
		return ErrFragmentMismatch
	}
	if bit(a.claimed, f.Index) {
		a.mu.Unlock()
		return nil
	}
	a.claimed[f.Index/64] |= 1 << (f.Index % 64)
	if f.Index == 0 {
		a.meta, a.haveMeta = Meta{Name: in.name, Type: in.typ, TotalSize: in.totalSize}, true
	}
	a.mu.Unlock()

	if len(in.data) > 0 {
		if _, err := a.out.WriteAt(in.data, int64(f.Index)*ChunkSize); err != nil {
			a.mu.Lock()
			a.claimed[f.Index/64] &^= 1 << (f.Index % 64)
			a.mu.Unlock()
			return fmt.Errorf("message: запись данных: %w", err)
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.written++
	a.done += int64(len(in.data))
	if a.progress != nil {
		a.progress(a.done, a.totalSize)
	}
	return nil
}

func (a *Assembler) decrypt(ctx context.Context, f *protocol.Fragment) (*inner, error) {
	s := a.params.Settings
	if s.Mode.NeedsIV() != (len(f.IV) == protocol.IVSize) || (!s.Mode.NeedsIV() && len(f.IV) != 0) {
		return nil, ErrFragmentMismatch
	}
	if len(f.Cipher) == 0 || len(f.Cipher)%cipher.BlockSize != 0 {
		return nil, ErrFragmentMismatch
	}
	opts := []cryptoctx.Option{cryptoctx.WithWorkers(1)}
	if s.Mode.NeedsIV() {
		opts = append(opts, cryptoctx.WithIV(f.IV))
	}
	cc, err := cryptoctx.NewWithCipher(a.cipher, s.Mode, s.Padding, opts...)
	if err != nil {
		return nil, err
	}
	plain, err := cc.DecryptContext(ctx, f.Cipher)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	in, err := parseInner(plain)
	if err != nil {
		return nil, err
	}
	if err := in.checkBinding(f); err != nil {
		return nil, err
	}
	return in, nil
}

// Finish возвращает метаданные, когда получены все фрагменты, иначе ErrMissingFragments.
func (a *Assembler) Finish() (Meta, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.total == 0 || a.written != a.total || !a.haveMeta {
		return Meta{}, ErrMissingFragments
	}
	return a.meta, nil
}

// ErrBadPadding — ошибка нарушенной набивки при расшифровании фрагмента.
var ErrBadPadding = padding.ErrBadPadding
