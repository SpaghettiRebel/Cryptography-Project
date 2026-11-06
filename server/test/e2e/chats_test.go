//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/bootstrap"
	"cryptochat/server/internal/rpc"
)

func randomValue(t *testing.T) []byte {
	t.Helper()
	v := make([]byte, 256)
	if _, err := rand.Read(v); err != nil {
		t.Fatal(err)
	}
	return v
}

// timed выполняет вызов и проверяет SC-007.
func timed(t *testing.T, name string, call func(context.Context) error) {
	t.Helper()
	ctx, cancel := callCtx()
	defer cancel()
	start := time.Now()
	err := call(ctx)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	t.Logf("SC-007: %s %v", name, took)
	if took > 200*time.Millisecond {
		t.Errorf("SC-007: %s занял %v", name, took)
	}
}

func TestChatsEndToEnd(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	s := &stack{t: t, certs: t.TempDir(), compose: filepath.Join(root, "deploy", "compose.yaml"), addr: "localhost:" + port}
	_, _ = s.run("down", "-v", "--remove-orphans")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := s.run("logs", "--no-color")
			t.Logf("журналы контейнеров:\n%s", out)
		}
		if s.conn != nil {
			_ = s.conn.Close()
		}
		_, _ = s.run("down", "-v", "--remove-orphans")
	})
	s.must("up", "--build", "-d")
	s.connect()
	s.waitHealthy(2 * time.Minute)
	chats := apiv1.NewChatsClient(s.conn)
	keys := apiv1.NewKeyExchangeClient(s.conn)

	// SC-001: весь сценарий через тестовый клиент.
	chatcli := buildChatcli(t, root)
	ca := filepath.Join(s.certs, bootstrap.ServerCertFile)
	cli := func(session, stdin string, args ...string) (string, error) {
		full := append([]string{"-addr", s.addr, "-ca", ca, "-session", session}, args...)
		cmd := exec.Command(chatcli, full...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	aliceFile, bobFile := filepath.Join(t.TempDir(), "alice"), filepath.Join(t.TempDir(), "bob")
	must := func(session, stdin, contains string, args ...string) string {
		t.Helper()
		out, err := cli(session, stdin, args...)
		if err != nil || !strings.Contains(out, contains) {
			t.Fatalf("chatcli %v: %v\n%s", args, err, out)
		}
		return out
	}
	fails := func(session, stdin, reason string, args ...string) {
		t.Helper()
		out, err := cli(session, stdin, args...)
		if err == nil || !strings.Contains(out, reason) {
			t.Fatalf("chatcli %v: ожидалась причина %s: %v\n%s", args, reason, err, out)
		}
	}
	const pw = "chat-e2e-passphrase"
	must(aliceFile, pw+"\n", "зарегистрирован", "register", "alice")
	must(aliceFile, pw+"\n", "вход выполнен", "login", "alice")
	must(bobFile, pw+"\n", "зарегистрирован", "register", "bob")
	must(bobFile, pw+"\n", "вход выполнен", "login", "bob")
	fails(aliceFile, "", rpc.ReasonNotContact, "chat-create", "bob", "RC6", "256", "CBC", "PKCS7")
	must(aliceFile, "", "запрос отправлен", "contact-request", "bob")
	must(bobFile, "", "готово", "contact-accept", "alice")
	fails(aliceFile, "", "поле: algorithm", "chat-create", "bob", "AES", "256", "CBC", "PKCS7")
	fails(aliceFile, "", "поле: key_bits", "chat-create", "bob", "RC6", "100", "CBC", "PKCS7")
	fails(aliceFile, "", "поле: key_bits", "chat-create", "bob", "RC6", "4294967552", "CBC", "PKCS7")
	fails(aliceFile, "", "поле: cipher_mode", "chat-create", "bob", "RC6", "256", "XTS", "PKCS7")
	fails(aliceFile, "", "поле: padding_mode", "chat-create", "bob", "RC6", "256", "CBC", "NONE")
	id := must(aliceFile, "", "", "chat-create", "bob", "RC6", "256", "RANDOM_DELTA", "ISO_10126")
	if len(id) != 32 {
		t.Fatalf("chat-create должен печатать только идентификатор: %q", id)
	}
	must(bobFile, "", "собеседник=alice", "chats")
	fails(bobFile, hex.EncodeToString(randomValue(t))+"\n", rpc.ReasonNotConnected, "key-publish", id)
	must(bobFile, "", "я_подключён=true", "chat-connect", id)
	must(bobFile, "", "значения пока нет", "key-get", id)
	fromAlice, fromBob := randomValue(t), randomValue(t)
	must(aliceFile, hex.EncodeToString(fromAlice)+"\n", "опубликовано", "key-publish", id)
	must(bobFile, hex.EncodeToString(fromBob)+"\r\n", "опубликовано", "key-publish", id)
	fails(aliceFile, hex.EncodeToString(fromBob), rpc.ReasonValueAlreadyPublished, "key-publish", id)
	fails(aliceFile, "abcd", rpc.ReasonInvalidPublicValue, "key-publish", id)
	fails(aliceFile, "не-hex", "шестнадцатеричным", "key-publish", id)
	if got := must(bobFile, "", "", "key-get", id); got != hex.EncodeToString(fromAlice) {
		t.Fatalf("значение изменилось при передаче: %s", got)
	}
	must(aliceFile, "", "готов=true", "chat-info", id)
	must(aliceFile, "", "я_подключён=false", "chat-disconnect", id)
	must(aliceFile, "", "я_подключён=true", "chat-connect", id)

	// SC-006: чат, подключения и значения переживают перезапуск.
	rawID, err := hex.DecodeString(id)
	if err != nil {
		t.Fatal(err)
	}
	aliceToken, err := os.ReadFile(aliceFile)
	if err != nil {
		t.Fatal(err)
	}
	bobToken, err := os.ReadFile(bobFile)
	if err != nil {
		t.Fatal(err)
	}
	asAlice := func(c context.Context) context.Context { return bearer(c, string(aliceToken)) }
	asBob := func(c context.Context) context.Context { return bearer(c, string(bobToken)) }
	ref := &apiv1.ChatRef{ChatId: rawID}

	s.must("down")
	s.must("up", "-d")
	s.waitHealthy(2 * time.Minute)
	ctx, cancel := callCtx()
	restored, err := chats.GetChat(asAlice(ctx), ref)
	cancel()
	if err != nil {
		t.Fatalf("SC-006: %v", err)
	}
	chat := restored.GetChat()
	if !chat.GetReady() || !chat.GetSelf().GetConnected() || !chat.GetPeer().GetConnected() ||
		chat.GetParams().GetCipherMode() != apiv1.CipherMode_CIPHER_MODE_RANDOM_DELTA {
		t.Fatalf("SC-006: после перезапуска %v", chat)
	}
	ctx, cancel = callCtx()
	peer, err := keys.GetPeerPublicValue(asAlice(ctx), ref)
	cancel()
	if err != nil || !bytes.Equal(peer.GetPublicValue(), fromBob) {
		t.Fatalf("SC-006: значение после перезапуска: %v", err)
	}

	// SC-007: каждая из 8 операций на прогретом сервере.
	var timedID []byte
	timed(t, "CreateChat", func(c context.Context) error {
		r, err := chats.CreateChat(asAlice(c), &apiv1.CreateChatRequest{PeerLogin: "bob", Params: &apiv1.ChatParams{
			Algorithm: apiv1.Algorithm_ALGORITHM_MAGENTA, KeyBits: 128,
			CipherMode: apiv1.CipherMode_CIPHER_MODE_CTR, PaddingMode: apiv1.PaddingMode_PADDING_MODE_ZEROS,
		}})
		if err == nil {
			timedID = r.GetChat().GetChatId()
		}
		return err
	})
	timedRef := &apiv1.ChatRef{ChatId: timedID}
	timedValue := randomValue(t)
	timed(t, "ListChats", func(c context.Context) error {
		_, err := chats.ListChats(asBob(c), &apiv1.ListChatsRequest{})
		return err
	})
	timed(t, "GetChat", func(c context.Context) error { _, err := chats.GetChat(asBob(c), timedRef); return err })
	timed(t, "ConnectChat", func(c context.Context) error { _, err := chats.ConnectChat(asBob(c), timedRef); return err })
	timed(t, "PublishPublicValue", func(c context.Context) error {
		_, err := keys.PublishPublicValue(asBob(c), &apiv1.PublishPublicValueRequest{ChatId: timedID, PublicValue: timedValue})
		return err
	})
	timed(t, "GetPeerPublicValue", func(c context.Context) error { _, err := keys.GetPeerPublicValue(asAlice(c), timedRef); return err })
	timed(t, "DisconnectChat", func(c context.Context) error { _, err := chats.DisconnectChat(asBob(c), timedRef); return err })
	timed(t, "CloseChat", func(c context.Context) error { _, err := chats.CloseChat(asAlice(c), timedRef); return err })

	// SC-008: сбой и восстановление хранилища.
	s.must("stop", "postgres")
	ctx, cancel = callCtx()
	failStart := time.Now()
	_, err = chats.GetChat(asAlice(ctx), ref)
	failTime := time.Since(failStart)
	cancel()
	if rpc.ReasonOf(err) != rpc.ReasonStoreUnavailable {
		t.Errorf("SC-008: %v (%q), код %v", err, rpc.ReasonOf(err), status.Code(err))
	}
	t.Logf("SC-008: ошибка за %v", failTime)
	if failTime > 5*time.Second {
		t.Errorf("SC-008: ответ занял %v", failTime)
	}
	s.must("start", "postgres")
	recoverStart := time.Now()
	for {
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		_, err = chats.GetChat(asAlice(ctx), ref)
		cancel()
		if err == nil {
			break
		}
		if time.Since(recoverStart) > time.Minute {
			t.Fatalf("SC-008: сервер не восстановился: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	recovery := time.Since(recoverStart)
	t.Logf("SC-008: восстановление за %v", recovery)
	if recovery > 30*time.Second {
		t.Errorf("SC-008: восстановление заняло %v", recovery)
	}

	// Значения хранятся, пока чат активен: иначе проверка ниже ничего не доказывает.
	dump := s.must("exec", "-T", "postgres", "pg_dump", "-U", "cryptochat", "cryptochat")
	if !strings.Contains(dump, hex.EncodeToString(fromAlice)) {
		t.Fatal("в дампе нет значения активного чата: проверка ничего не значит")
	}

	// Закрытие и SC-005: значений нет ни в журналах, ни в базе.
	must(bobFile, "", "закрыт", "chat-close", id)
	fails(aliceFile, "", rpc.ReasonChatClosed, "chat-connect", id)
	fails(aliceFile, "", rpc.ReasonChatClosed, "key-get", id)
	ctx, cancel = callCtx()
	_, err = keys.GetPeerPublicValue(asAlice(ctx), ref)
	cancel()
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("значение после закрытия: %v", err)
	}
	dump = s.must("exec", "-T", "postgres", "pg_dump", "-U", "cryptochat", "cryptochat")
	logs := s.must("logs", "--no-color")
	for name, haystack := range map[string]string{"база": dump, "журналы": logs} {
		for _, value := range [][]byte{fromAlice, fromBob, timedValue} {
			if strings.Contains(haystack, hex.EncodeToString(value)) {
				t.Errorf("SC-005: %s содержат открытое значение", name)
			}
		}
	}
	if strings.Contains(logs, id) {
		t.Error("журналы содержат идентификатор чата")
	}
}
