package delivery

import (
	"strconv"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
	"cryptochat/core/modes"
)

// modeOf и paddingOf переводят номера контракта (1–7, 1–4) в значения core/modes (0–6, 0–3).
func modeOf(n int) modes.CipherMode     { return modes.CipherMode(n - 1) }
func paddingOf(n int) modes.PaddingMode { return modes.PaddingMode(n - 1) }

// ProgressValue — сделано из всего; 0 из 0 — пустые данные.
type ProgressValue struct {
	Done  int64
	Total int64
}

// ViewError — причина состояния сообщения для пользователя.
type ViewError struct {
	Code    string
	Message string
}

// MessageView — сообщение для интерфейса. Шифротекста, IV, ключей и message_id здесь нет.
type MessageView struct {
	Seq         int64
	ChatID      [16]byte
	Direction   string // out | in
	Kind        string // pending | text | image | file
	CreatedAt   int64
	State       string
	Text        string
	FileName    string
	Size        int64
	MediaURL    string
	Error       *ViewError
	Encrypt     *ProgressValue
	Send        *ProgressValue
	Receive     *ProgressValue
	Decrypt     *ProgressValue
	Cancellable bool
	Retryable   bool
}

var stateNames = map[int]string{
	localdb.StateQueued: "QUEUED", localdb.StateSending: "SENDING", localdb.StateWaiting: "WAITING",
	localdb.StateSent: "SENT", localdb.StateCancelled: "CANCELLED", localdb.StateFailed: "FAILED",
	localdb.StateUndelivered: "UNDELIVERED", localdb.StateReceiving: "RECEIVING",
	localdb.StateDecryptPending: "DECRYPT_PENDING", localdb.StateDecrypting: "DECRYPTING",
	localdb.StateDecryptCancelled: "DECRYPT_CANCELLED", localdb.StateDone: "DONE",
	localdb.StateCancelledByPeer: "CANCELLED_BY_PEER", localdb.StateCorrupted: "CORRUPTED",
	localdb.StateKeyMissing: "KEY_MISSING",
}

var kindNames = map[int]string{localdb.KindPending: "pending", localdb.KindText: "text", localdb.KindFile: "file",
	localdb.KindImage: "image"}

// MediaURL — адрес картинки для окна.
func MediaURL(seq int64) string { return "/media/" + strconv.FormatInt(seq, 10) }

// View строит вид сообщения из строки базы и живого прогресса.
func (e *Engine) View(m localdb.Message) MessageView {
	v := MessageView{Seq: m.Seq, ChatID: m.ChatID, Direction: "in", Kind: kindNames[m.Kind], CreatedAt: m.CreatedAt,
		State: stateNames[m.State], Size: m.Size}
	if v.Kind == "" {
		v.Kind = "pending"
	}
	if m.Direction == localdb.DirOut {
		v.Direction = "out"
	}
	switch m.Kind {
	case localdb.KindText:
		v.Text = m.Text
	case localdb.KindFile, localdb.KindImage:
		v.FileName = m.FileName
	}
	if m.Kind == localdb.KindImage && m.State == localdb.StateDone {
		v.MediaURL = MediaURL(m.Seq)
	}
	if m.Direction == localdb.DirIn && m.State != localdb.StateDone {
		v.Size = -1
	}
	if m.ErrorCode != "" {
		v.Error = &ViewError{Code: m.ErrorCode, Message: apperr.New(m.ErrorCode).Message}
	}
	switch m.State {
	case localdb.StateQueued, localdb.StateSending, localdb.StateWaiting:
		size := max(m.Size, 0)
		enc := &ProgressValue{Done: min(int64(m.EncryptedCount)*localdb.ChunkSize, size), Total: size}
		if d, t, ok := e.progress.Get(m.Seq, StageEncrypt); ok {
			enc = &ProgressValue{Done: d, Total: t}
		}
		v.Encrypt = enc
		v.Send = &ProgressValue{Done: int64(m.SentCount), Total: int64(m.Total)}
		v.Cancellable = true
	case localdb.StateReceiving:
		v.Receive = &ProgressValue{Done: int64(m.ReceivedCount), Total: int64(m.Total)}
		v.Cancellable = true
	case localdb.StateDecryptPending:
		v.Cancellable = true
		v.Retryable = m.ErrorCode != ""
	case localdb.StateDecrypting:
		v.Decrypt = &ProgressValue{Done: 0, Total: 1}
		if d, t, ok := e.progress.Get(m.Seq, StageDecrypt); ok {
			v.Decrypt = &ProgressValue{Done: d, Total: t}
		}
		v.Cancellable = true
	case localdb.StateDecryptCancelled:
		v.Retryable = true
	}
	return v
}
