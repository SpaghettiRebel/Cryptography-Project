//go:build e2e

package e2e

import (
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	apiv1 "cryptochat/client/internal/api/v1"
	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/ciphers"
	"cryptochat/client/internal/service"
	"cryptochat/core/cipher"
)

func chatsOnServer(t *testing.T, r *raw) int {
	t.Helper()
	ctx, cancel := r.ctx()
	defer cancel()
	resp, err := apiv1.NewChatsClient(r.conn).ListChats(ctx, &apiv1.ListChatsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return len(resp.GetChats())
}

// История 3, сценарии 1–2: доступный алгоритм создаёт чат, видимый у обоих с теми же параметрами;
// недоступный отклоняется до сервера. Ветка выбирается по составу сборки.
func TestChatsCreateByAvailability(t *testing.T) {
	a, b := user(t, "cha"), user(t, "chb")
	befriend(t, a, b)
	r := rawLogin(t, a.login, a.password)
	for _, alg := range []cipher.Algorithm{cipher.MAGENTA, cipher.RC6} {
		before := chatsOnServer(t, r)
		c, err := a.svc.CreateChat(service.CreateChatRequest{Peer: b.login, Algorithm: alg.String(), KeyBits: 192, CipherMode: "OFB", Padding: "ANSI_X923"})
		if !ciphers.Info(alg).Available {
			if apperr.CodeOf(err) != apperr.AlgorithmUnavailable {
				t.Fatalf("%s: %v", alg, err)
			}
			if chatsOnServer(t, r) != before {
				t.Fatalf("%s: чат появился на сервере", alg)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", alg, err)
		}
		d := eventually(t, 5*time.Second, "чат у собеседника", func() bool {
			got, ok := b.chat(c.ID)
			return ok && got.Algorithm == alg && got.KeyBits == 192 && got.Mode == 5 && got.Padding == 2 &&
				strings.EqualFold(got.Creator, a.login)
		})
		t.Logf("SC-007: %s — чат виден у собеседника через %v", alg, d.Round(time.Millisecond))
	}
}

// История 3, сценарии 1 и 4: все 3 × 7 × 4 сочетания длины, режима и набивки видны у второго участника
// с теми же параметрами.
func TestChatsAllParameterCombinations(t *testing.T) {
	a, b := user(t, "cma"), user(t, "cmb")
	befriend(t, a, b)
	r := rawLogin(t, a.login, a.password)
	type want struct {
		alg              cipher.Algorithm
		bits, mode, pads int
	}
	created := map[[16]byte]want{}
	viaService := 0
	n := 0
	for _, bits := range []int{128, 192, 256} {
		for mode := 1; mode <= 7; mode++ {
			for pad := 1; pad <= 4; pad++ {
				alg := cipher.MAGENTA
				if n%2 == 1 {
					alg = cipher.RC6
				}
				n++
				var id [16]byte
				if ciphers.Info(alg).Available {
					c, err := a.svc.CreateChat(service.CreateChatRequest{Peer: b.login, Algorithm: alg.String(), KeyBits: bits,
						CipherMode: ciphers.ModeID(mode), Padding: ciphers.PaddingID(pad)})
					if err != nil {
						t.Fatalf("%v %d %d %d: %v", alg, bits, mode, pad, err)
					}
					id = c.ID
					viaService++
				} else {
					id = r.createChat(b.login, apiv1.Algorithm(alg), bits, apiv1.CipherMode(mode), apiv1.PaddingMode(pad))
				}
				created[id] = want{alg, bits, mode, pad}
			}
		}
	}
	t.Logf("создано чатов: %d, через сервис клиента: %d", len(created), viaService)
	eventually(t, 15*time.Second, "все чаты у собеседника", func() bool {
		seen := 0
		for _, c := range b.svc.Snapshot().Chats {
			if w, ok := created[c.ID]; ok && w == (want{c.Algorithm, c.KeyBits, c.Mode, c.Padding}) {
				seen++
			}
		}
		return seen == len(created)
	})
	for _, c := range a.svc.Snapshot().Chats {
		if _, ok := created[c.ID]; ok && c.AlgorithmNote == "" && !ciphers.Info(c.Algorithm).Available {
			t.Fatal("нет пояснения у недоступного алгоритма")
		}
	}
	// Уборка: закрыть все чаты, чтобы не нагружать следующие тесты.
	for id := range created {
		if err := a.svc.CloseChat(hex.EncodeToString(id[:])); err != nil {
			t.Fatal(err)
		}
	}
}

// История 3, сценарии 5–6 и пограничные случаи: закрытие одним, одновременное закрытие, закрытие пока
// приложение собеседника выключено.
func TestChatsClose(t *testing.T) {
	a, b := user(t, "cla"), user(t, "clb")
	befriend(t, a, b)
	r := rawLogin(t, a.login, a.password)
	newChat := func() [16]byte {
		id := r.createChat(b.login, apiv1.Algorithm_ALGORITHM_RC6, 128, apiv1.CipherMode_CIPHER_MODE_CBC, apiv1.PaddingMode_PADDING_MODE_PKCS7)
		eventually(t, 5*time.Second, "чат у обоих", func() bool {
			_, okA := a.chat(id)
			_, okB := b.chat(id)
			return okA && okB
		})
		return id
	}

	// Сценарий 6: закрытие одним, у второго уведомление и исчезновение не позже 5 с.
	id := newChat()
	if err := b.svc.CloseChat(hex.EncodeToString(id[:])); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.chat(id); ok {
		t.Fatal("закрытый чат остался у закрывшего")
	}
	d := eventually(t, 5*time.Second, "уведомление и исчезновение у второго", func() bool {
		_, ok := a.chat(id)
		return !ok && len(a.rec.notices("CHAT_CLOSED_BY_PEER")) == 1
	})
	t.Logf("SC-007: закрытие собеседником видно через %v", d.Round(time.Millisecond))

	// Одновременное закрытие обоими: ошибок нет.
	id = newChat()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, p := range []*participant{a, b} {
		wg.Add(1)
		go func(i int, p *participant) {
			defer wg.Done()
			errs[i] = p.svc.CloseChat(hex.EncodeToString(id[:]))
		}(i, p)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("одновременное закрытие: %v / %v", errs[0], errs[1])
	}
	for _, p := range []*participant{a, b} {
		if _, ok := p.chat(id); ok {
			t.Fatal("чат остался после одновременного закрытия")
		}
	}

	// Закрытие, пока приложение собеседника выключено: после входа чата нет среди активных.
	id = newChat()
	b.svc.Close()
	if err := a.svc.CloseChat(hex.EncodeToString(id[:])); err != nil {
		t.Fatal(err)
	}
	b.open()
	b.signIn()
	if _, ok := b.chat(id); ok {
		t.Fatal("закрытый без участника чат показан активным")
	}
}
