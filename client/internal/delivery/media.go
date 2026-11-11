package delivery

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
)

// ServeMedia отдаёт картинку готового входящего сообщения: GET /media/<seq>. Тип — по содержимому,
// nosniff и no-store; всё остальное — 404 без тела. Куски читаются из базы пачками.
func (e *Engine) ServeMedia(w http.ResponseWriter, r *http.Request) {
	seq, ok := mediaSeq(r)
	if !ok {
		NotFound(w)
		return
	}
	m, ok, err := e.cfg.Store.Message(seq)
	if err != nil || !ok || m.Kind != localdb.KindImage || m.State != localdb.StateDone || m.MediaType == "" {
		NotFound(w)
		return
	}
	h := w.Header()
	h.Set("Content-Type", m.MediaType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.FormatInt(m.Size, 10))
	w.WriteHeader(http.StatusOK)
	after := -1
	for {
		list, err := e.cfg.Store.Chunks(seq, after, localdb.BatchRows)
		if err != nil || len(list) == 0 {
			return
		}
		for _, c := range list {
			if _, err := w.Write(c.Data); err != nil {
				return
			}
		}
		after = list[len(list)-1].Idx
	}
}

// NotFound отвечает 404 без тела.
func NotFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
}

func mediaSeq(r *http.Request) (int64, bool) {
	if r.Method != http.MethodGet {
		return 0, false
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/media/")
	if !ok {
		return 0, false
	}
	seq, err := strconv.ParseInt(rest, 10, 64)
	return seq, err == nil && seq > 0
}

// IsMediaPath сообщает, что запрос относится к обработчику картинок.
func IsMediaPath(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/media/") }

// SaveFile записывает файл или картинку из истории в path: во временный файл в той же папке и затем
// переименованием. Куски читаются пачками.
func (e *Engine) SaveFile(seq int64, path string) error {
	m, ok, err := e.cfg.Store.Message(seq)
	if err != nil {
		return apperr.New(apperr.LocalWrite)
	}
	if !ok || m.Kind == localdb.KindControl {
		return apperr.New(apperr.MessageNotFound)
	}
	saveable := (m.Kind == localdb.KindFile || m.Kind == localdb.KindImage) &&
		(m.State == localdb.StateDone || (m.State == localdb.StateSent && m.ChunksComplete))
	if !saveable {
		return apperr.New(apperr.NotAFile)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cryptochat-*.part")
	if err != nil {
		return apperr.New(apperr.FileWrite)
	}
	name := tmp.Name()
	fail := func() error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return apperr.New(apperr.FileWrite)
	}
	var written int64
	after := -1
	for {
		list, err := e.cfg.Store.Chunks(seq, after, localdb.BatchRows)
		if err != nil {
			return fail()
		}
		if len(list) == 0 {
			break
		}
		for _, c := range list {
			if _, err := tmp.WriteAt(c.Data, int64(c.Idx)*localdb.ChunkSize); err != nil {
				return fail()
			}
			written += int64(len(c.Data))
		}
		after = list[len(list)-1].Idx
	}
	if written != m.Size || tmp.Truncate(m.Size) != nil || tmp.Sync() != nil {
		return fail()
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return apperr.New(apperr.FileWrite)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return apperr.New(apperr.FileWrite)
	}
	return nil
}
