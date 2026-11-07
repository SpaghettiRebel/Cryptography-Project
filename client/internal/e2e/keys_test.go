//go:build e2e

package e2e

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	apiv1 "cryptochat/client/internal/api/v1"
	"cryptochat/client/internal/ciphers"
	"cryptochat/client/internal/keyagree"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/profile"
	"cryptochat/client/internal/service"
	"cryptochat/core/cipher"
)

// createChat создаёт чат сервисом, если алгоритм есть в сборке, иначе напрямую через API.
func createChat(t *testing.T, creator, peer *participant, alg cipher.Algorithm, bits int) [16]byte {
	t.Helper()
	if ciphers.Info(alg).Available && creator.svc.Snapshot().Session != nil {
		c, err := creator.svc.CreateChat(service.CreateChatRequest{Peer: peer.login, Algorithm: alg.String(), KeyBits: bits, CipherMode: "CBC", Padding: "PKCS7"})
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	return rawLogin(t, creator.login, creator.password).createChat(peer.login, apiv1.Algorithm(alg), bits,
		apiv1.CipherMode_CIPHER_MODE_CBC, apiv1.PaddingMode_PADDING_MODE_PKCS7)
}

func keyState(p *participant, id [16]byte) keyagree.State {
	c, ok := p.chat(id)
	if !ok {
		return ""
	}
	return c.KeyState
}

func waitReady(t *testing.T, id [16]byte, ps ...*participant) time.Duration {
	t.Helper()
	return eventually(t, 5*time.Second, "ключ согласован у обоих", func() bool {
		for _, p := range ps {
			if keyState(p, id) != keyagree.Ready {
				return false
			}
		}
		return true
	})
}

func connect(t *testing.T, p *participant, id [16]byte) {
	t.Helper()
	if _, err := p.svc.ConnectChat(hex.EncodeToString(id[:])); err != nil {
		t.Fatalf("подключение %s: %v", p.login, err)
	}
}

// storedKey закрывает сервис участника (снимая блокировку профиля) и читает ключ из его базы.
func storedKey(t *testing.T, p *participant, id [16]byte) []byte {
	t.Helper()
	p.svc.Close()
	db, err := localdb.Open(profile.DBPath(p.home, address, userID(t, p)))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key, ok, err := db.SessionKey(id)
	if err != nil || !ok {
		t.Fatalf("ключа нет в базе %s: %v", p.login, err)
	}
	if _, ok, _ := db.DHSecret(id); ok {
		t.Fatalf("секрет %s не удалён после выработки ключа", p.login)
	}
	rememberSecret(key)
	return key
}

// История 4, сценарии 1–2 и SC-003: все длины ключа и оба порядка публикации дают одинаковые ключи.
func TestKeysAllLengthsAndOrders(t *testing.T) {
	for _, bits := range []int{128, 192, 256} {
		for _, order := range []string{"создатель первым", "приглашённый первым"} {
			t.Run(fmt.Sprintf("%d %s", bits, order), func(t *testing.T) {
				alg := cipher.RC6
				if bits == 192 {
					alg = cipher.MAGENTA
				}
				a, b := user(t, "ka"), user(t, "kb")
				befriend(t, a, b)
				var id [16]byte
				if order == "создатель первым" {
					id = createChat(t, a, b, alg, bits)
					eventually(t, 5*time.Second, "значение создателя", func() bool { return keyState(a, id) == keyagree.WaitingPeer })
					eventually(t, 5*time.Second, "чат у приглашённого", func() bool { _, ok := b.chat(id); return ok })
					connect(t, b, id)
				} else {
					a.svc.Close()
					id = createChat(t, a, b, alg, bits)
					eventually(t, 5*time.Second, "чат у приглашённого", func() bool { _, ok := b.chat(id); return ok })
					connect(t, b, id)
					if keyState(b, id) != keyagree.WaitingPeer {
						t.Fatalf("приглашённый: %s", keyState(b, id))
					}
					a.open()
					a.signIn()
				}
				d := waitReady(t, id, a, b)
				t.Logf("SC-002/SC-003: %v/%d, %s: READY у обоих через %v после второй публикации", alg, bits, order, d.Round(time.Millisecond))
				ka, kb := storedKey(t, a, id), storedKey(t, b, id)
				if len(ka) != bits/8 || !bytes.Equal(ka, kb) {
					t.Fatalf("ключи сторон различаются или неверной длины: %d/%d байт", len(ka), len(kb))
				}
			})
		}
	}
}

// История 4, сценарии 3–5: перезапуск между публикацией и появлением значения собеседника, перезапуск
// с готовым ключом, отключение и повторное подключение.
func TestKeysRestartAndReconnect(t *testing.T) {
	a, b := user(t, "kra"), user(t, "krb")
	befriend(t, a, b)
	id := createChat(t, a, b, cipher.RC6, 256)
	eventually(t, 5*time.Second, "значение создателя", func() bool { return keyState(a, id) == keyagree.WaitingPeer })
	// Перезапуск до появления значения собеседника.
	a.restart()
	a.signIn()
	if st := keyState(a, id); st != keyagree.WaitingPeer {
		t.Fatalf("после перезапуска: %s", st)
	}
	eventually(t, 5*time.Second, "чат у приглашённого", func() bool { _, ok := b.chat(id); return ok })
	connect(t, b, id)
	waitReady(t, id, a, b)

	// Перезапуск с готовым ключом: READY без обмена значениями.
	a.restart()
	a.signIn()
	if st := keyState(a, id); st != keyagree.Ready {
		t.Fatalf("после перезапуска с ключом: %s", st)
	}
	time.Sleep(2500 * time.Millisecond)
	if n := a.spy.exchangeCalls(); n != 0 {
		t.Fatalf("вызовов обмена значениями после перезапуска с ключом: %d", n)
	}

	// Отключение сохраняет ключ, повторное подключение не согласует заново.
	c, err := a.svc.DisconnectChat(hex.EncodeToString(id[:]))
	if err != nil || c.SelfConnected || c.KeyState != keyagree.Ready {
		t.Fatalf("отключение: %+v %v", c, err)
	}
	eventually(t, 5*time.Second, "собеседник видит отключение", func() bool { c, _ := b.chat(id); return !c.PeerConnected })
	c, err = a.svc.ConnectChat(hex.EncodeToString(id[:]))
	if err != nil || !c.SelfConnected || c.KeyState != keyagree.Ready {
		t.Fatalf("повторное подключение: %+v %v", c, err)
	}
	if n := a.spy.exchangeCalls(); n != 0 {
		t.Fatalf("повторное согласование: %d вызовов", n)
	}
	ka, kb := storedKey(t, a, id), storedKey(t, b, id)
	if !bytes.Equal(ka, kb) {
		t.Fatal("ключи различаются после перезапусков")
	}
}

// История 4, сценарий 6: профиль удалён после публикации — KEY_LOST, другое значение не публикуется.
func TestKeysLostProfile(t *testing.T) {
	a, b := user(t, "kla"), user(t, "klb")
	befriend(t, a, b)
	id := createChat(t, a, b, cipher.MAGENTA, 128)
	eventually(t, 5*time.Second, "значение создателя", func() bool { return keyState(a, id) == keyagree.WaitingPeer })
	eventually(t, 5*time.Second, "чат у приглашённого", func() bool { _, ok := b.chat(id); return ok })
	before := rawLogin(t, b.login, b.password).peerValue(id)
	if len(before) != 256 {
		t.Fatal("значение создателя не опубликовано")
	}
	a.svc.Close()
	if err := os.RemoveAll(filepath.Join(a.home, "profiles")); err != nil {
		t.Fatal(err)
	}
	a.open()
	a.signIn()
	if st := keyState(a, id); st != keyagree.KeyLost {
		t.Fatalf("состояние: %s", st)
	}
	if n := a.spy.publishCalls(); n != 0 {
		t.Fatalf("опубликовано другое значение: %d вызовов", n)
	}
	after := rawLogin(t, b.login, b.password).peerValue(id)
	if !bytes.Equal(before, after) {
		t.Fatal("значение на сервере изменилось")
	}
	// Приложение работает: чат можно закрыть.
	if err := a.svc.CloseChat(hex.EncodeToString(id[:])); err != nil {
		t.Fatal(err)
	}
}
