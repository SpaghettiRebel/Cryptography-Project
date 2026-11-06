package message

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"

	"cryptochat/core/cipher"
	"cryptochat/core/cryptoctx"
	"cryptochat/core/protocol"
)

// sealConfig — внутренние настройки. Подменяются только в тестах пакета; публичный Seal всегда
// использует настоящий алгоритм, crypto/rand и обычное окно.
type sealConfig struct {
	cipher  cipher.BlockCipher // с заданным ключом
	rand    io.Reader
	workers int
	track   func(delta int) // учёт живых объёмов фрагментов для проверки памяти
}

// Seal читает meta.TotalSize байт из r и передаёт зашифрованные фрагменты в emit строго по порядку,
// из одной горутины. Куски шифруются независимо и параллельно, каждый со своим IV.
func Seal(ctx context.Context, p Params, messageID [protocol.IDSize]byte, meta Meta, r io.Reader,
	emit func(*protocol.Fragment) error, progress Progress) error {
	if err := p.validate(); err != nil {
		return err
	}
	c, err := p.keyedCipher()
	if err != nil {
		return err
	}
	return seal(ctx, sealConfig{cipher: c, rand: rand.Reader, workers: defaultWorkers()}, p, messageID, meta, r, emit, progress)
}

type sealResult struct {
	fragment *protocol.Fragment
	dataLen  int
	err      error
}

func seal(parent context.Context, cfg sealConfig, p Params, messageID [protocol.IDSize]byte, meta Meta, r io.Reader,
	emit func(*protocol.Fragment) error, progress Progress) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if err := p.validate(); err != nil {
		return err
	}
	if len(meta.Name) > MaxNameLen || len(meta.Type) > MaxTypeLen {
		return ErrInvalidMeta
	}
	if meta.TotalSize < 0 {
		return ErrSizeMismatch
	}
	total := fragmentCount(meta.TotalSize)
	if total > protocol.MaxTotal {
		return ErrSizeMismatch
	}
	track := cfg.track
	if track == nil {
		track = func(int) {}
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var (
		errMu    sync.Mutex
		firstErr error
	)
	fail := func(err error) {
		errMu.Lock()
		defer errMu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}

	// Слот занимается до чтения куска и освобождается после выдачи фрагмента, поэтому в обработке
	// никогда не больше cfg.workers фрагментов.
	slots := make(chan struct{}, cfg.workers)
	queue := make(chan chan sealResult, cfg.workers)
	emitted := make(chan struct{})

	go func() {
		defer close(emitted)
		var done int64
		for ch := range queue {
			res := <-ch
			if res.err != nil {
				fail(res.err)
			} else if ctx.Err() == nil {
				if err := emit(res.fragment); err != nil {
					fail(err)
				} else {
					done += int64(res.dataLen)
					if progress != nil {
						progress(done, meta.TotalSize)
					}
				}
			}
			if res.fragment != nil {
				track(-1)
			}
			<-slots
		}
	}()

	var workers sync.WaitGroup
	remaining := meta.TotalSize
loop:
	for i := int64(0); i < total; i++ {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		dataLen := int(min(remaining, ChunkSize))
		remaining -= int64(dataLen)

		in := inner{chatID: p.ChatID, messageID: messageID, index: uint32(i), totalSize: meta.TotalSize}
		if i == 0 {
			in.name, in.typ = meta.Name, meta.Type
		}
		// Чтение, IV и заполнение идут последовательно, поэтому порядок обращений к источнику
		// случайности не зависит от числа рабочих.
		size := in.headerSize() + dataLen
		buf := make([]byte, size, size+cipher.BlockSize)
		track(1)
		in.putHeader(buf, dataLen)
		if _, err := io.ReadFull(r, buf[in.headerSize():]); err != nil {
			track(-1)
			<-slots
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				fail(ErrSizeMismatch)
			} else {
				fail(fmt.Errorf("message: чтение данных: %w", err))
			}
			break loop
		}
		plain, err := addFill(buf, cfg.rand)
		var iv []byte
		if err == nil && p.Settings.Mode.NeedsIV() {
			iv = make([]byte, protocol.IVSize)
			if _, rerr := io.ReadFull(cfg.rand, iv); rerr != nil {
				err = fmt.Errorf("message: источник случайности: %w", rerr)
			}
		}
		if err != nil {
			track(-1)
			<-slots
			fail(err)
			break loop
		}

		ch := make(chan sealResult, 1)
		queue <- ch
		workers.Add(1)
		go func(index uint32, plain, iv []byte, dataLen int) {
			defer workers.Done()
			track(2)
			frag, err := encryptFragment(ctx, cfg.cipher, p, messageID, index, uint32(total), plain, iv)
			track(-2)
			if err != nil {
				track(-1)
				ch <- sealResult{err: err}
				return
			}
			ch <- sealResult{fragment: frag, dataLen: dataLen}
		}(uint32(i), plain, iv, dataLen)
	}

	if ctx.Err() == nil {
		// Данные длиннее заявленного размера.
		var probe [1]byte
		switch _, err := io.ReadFull(r, probe[:]); {
		case err == nil:
			fail(ErrSizeMismatch)
		case !errors.Is(err, io.EOF):
			fail(fmt.Errorf("message: чтение данных: %w", err))
		}
	}
	close(queue)
	<-emitted
	workers.Wait()

	// Все горутины уже остановлены.
	if err := parent.Err(); err != nil {
		return err
	}
	errMu.Lock()
	defer errMu.Unlock()
	return firstErr
}

func encryptFragment(ctx context.Context, c cipher.BlockCipher, p Params, messageID [protocol.IDSize]byte,
	index, total uint32, plain, iv []byte) (*protocol.Fragment, error) {
	opts := []cryptoctx.Option{cryptoctx.WithWorkers(1)}
	if iv != nil {
		opts = append(opts, cryptoctx.WithIV(iv))
	}
	cc, err := cryptoctx.NewWithCipher(c, p.Settings.Mode, p.Settings.Padding, opts...)
	if err != nil {
		return nil, err
	}
	ct, err := cc.EncryptContext(ctx, plain)
	if err != nil {
		return nil, err
	}
	return &protocol.Fragment{
		ChatID: p.ChatID, MessageID: messageID,
		Index: index, Total: total,
		IV: iv, Cipher: ct,
	}, nil
}
