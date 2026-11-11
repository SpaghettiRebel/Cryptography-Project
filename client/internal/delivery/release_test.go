//go:build !devcipher

package delivery

import (
	"testing"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
	"cryptochat/core/protocol"
)

// В сборке без алгоритмов отправка не уходит на сервер, а принятые части хранятся до сборки, где
// алгоритм есть.
func TestReleaseBuild(t *testing.T) {
	p := newPair(t, 2, 3, 256)
	p.a.receive()
	p.b.receive()
	v, err := p.a.e.SendText(p.id, "текст")
	if err != nil {
		t.Fatal(err)
	}
	m := p.a.waitState(v.Seq, 5*time.Second, localdb.StateFailed)
	if m.ErrorCode != apperr.AlgorithmUnavailable {
		t.Fatalf("причина %q", m.ErrorCode)
	}
	if n := p.a.api.Count("SendFragment"); n != 0 {
		t.Fatalf("вызовов SendFragment: %d", n)
	}
	f := &protocol.Fragment{ChatID: p.id, MessageID: [16]byte{9}, Index: 0, Total: 1, IV: make([]byte, 16), Cipher: make([]byte, 32)}
	wire, _ := f.Marshal()
	p.srv.Inject(p.id, "bob", wire)
	got := p.b.waitIncoming(1, 5*time.Second, localdb.StateDecryptPending)
	eventually(t, 5*time.Second, "причина ALGORITHM_UNAVAILABLE", func() bool {
		return p.b.msg(got[0].Seq).ErrorCode == apperr.AlgorithmUnavailable
	})
	if frs, _ := p.b.db.InFragments(got[0].Seq, -1, 16); len(frs) != 1 {
		t.Fatal("шифротекст не сохранён")
	}
}
