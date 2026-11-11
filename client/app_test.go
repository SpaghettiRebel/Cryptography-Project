package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"cryptochat/client/internal/apitest"
	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/client/internal/service"
	"cryptochat/core/cipher"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Каждый экспортируемый метод App первым оператором ставит defer a.guard(&err), а последним
// результатом возвращает error с именем err.
func TestEveryMethodGuarded(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || !fn.Name.IsExported() {
			continue
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok || star.X.(*ast.Ident).Name != "App" {
			continue
		}
		count++
		res := fn.Type.Results
		if res == nil || len(res.List) == 0 {
			t.Errorf("%s: нет результата error", fn.Name.Name)
			continue
		}
		last := res.List[len(res.List)-1]
		if id, ok := last.Type.(*ast.Ident); !ok || id.Name != "error" || len(last.Names) == 0 || last.Names[len(last.Names)-1].Name != "err" {
			t.Errorf("%s: последний результат должен быть err error", fn.Name.Name)
		}
		if len(fn.Body.List) == 0 {
			t.Errorf("%s: пустое тело", fn.Name.Name)
			continue
		}
		d, ok := fn.Body.List[0].(*ast.DeferStmt)
		if !ok {
			t.Errorf("%s: первый оператор не defer", fn.Name.Name)
			continue
		}
		sel, ok := d.Call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "guard" || sel.X.(*ast.Ident).Name != "a" || len(d.Call.Args) != 1 {
			t.Errorf("%s: первый оператор не defer a.guard(&err)", fn.Name.Name)
			continue
		}
		u, ok := d.Call.Args[0].(*ast.UnaryExpr)
		if !ok || u.Op != token.AND || u.X.(*ast.Ident).Name != "err" {
			t.Errorf("%s: guard получает не &err", fn.Name.Name)
		}
	}
	if count != 28 {
		t.Fatalf("методов App: %d, по контракту 28", count)
	}
}

func testApp(t *testing.T) *App {
	t.Helper()
	srv := apitest.NewServer()
	svc := service.New(service.Config{
		Root:   t.TempDir(),
		Dial:   func(string, string) (rpcclient.API, error) { return srv.Client(), nil },
		Logger: log.New(io.Discard, "", 0),
	})
	t.Cleanup(svc.Close)
	a := NewApp(svc)
	a.log = log.New(io.Discard, "", 0)
	return a
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var v map[string]string
	if err == nil {
		return ""
	}
	if e := json.Unmarshal([]byte(err.Error()), &v); e != nil {
		t.Fatalf("ошибка не JSON: %q", err.Error())
	}
	return v["code"]
}

// Паника в методе возвращается ошибкой INTERNAL, а следующий вызов работает.
func TestPanicBecomesInternal(t *testing.T) {
	a := testApp(t)
	svc := a.svc
	a.svc = nil // любое обращение к сервису — паника
	if _, err := a.GetSnapshot(); codeOf(t, err) != apperr.Internal {
		t.Fatalf("паника не перехвачена: %v", err)
	}
	if _, err := a.ChooseCertificate(); codeOf(t, err) != apperr.Internal {
		t.Fatalf("диалог без окна: %v", err)
	}
	a.svc = svc
	s, err := a.GetSnapshot()
	if err != nil || s.Session != nil {
		t.Fatalf("следующий вызов: %+v %v", s, err)
	}
	if _, err := a.ChatOptions(); err != nil {
		t.Fatal(err)
	}
}

// Ошибка не из apperr уходит в интерфейс как INTERNAL, ошибка apperr — как есть.
func TestErrorsAreJSON(t *testing.T) {
	a := testApp(t)
	if err := a.Logout(); err != nil {
		t.Fatalf("выход без входа: %v", err)
	}
	if err := a.AcceptContact("bob"); codeOf(t, err) != apperr.NotLoggedIn {
		t.Fatalf("%v", err)
	}
	if err := a.Register("x", "short"); codeOf(t, err) != apperr.InvalidLogin {
		t.Fatalf("%v", err)
	}
}

// JSON всех типов, которые получает интерфейс, не содержит сеансового ключа, секретного и открытого
// значений Диффи-Хеллмана ни в hex, ни в base64.
func TestDTOsHaveNoSecrets(t *testing.T) {
	srv := apitest.NewServer()
	var (
		mu      sync.Mutex
		secrets [][]byte
		events  []string
	)
	newSide := func(login string) (*App, *apitest.Client, *localdb.DB) {
		api := srv.Client()
		var db *localdb.DB // записывается до первого вызова Hook
		svc := service.New(service.Config{
			Root: t.TempDir(),
			Dial: func(string, string) (rpcclient.API, error) { return api, nil },
			OpenStore: func(path string) (*localdb.DB, error) {
				d, err := localdb.Open(path)
				db = d
				return d, err
			},
			Interval: 50 * time.Millisecond,
			Logger:   log.New(io.Discard, "", 0),
		})
		t.Cleanup(svc.Close)
		svc.Subscribe(service.EmitterFunc(func(event string, data any) {
			b, _ := json.Marshal(eventView(data))
			mu.Lock()
			events = append(events, string(b))
			mu.Unlock()
		}))
		a := NewApp(svc)
		a.log = log.New(io.Discard, "", 0)
		if err := a.Register(login, "password-123"); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Login(login, "password-123"); err != nil {
			t.Fatal(err)
		}
		api.SetHook(func(m string) error {
			if m == "PublishPublicValue" && db != nil {
				chats, _ := db.KnownActiveChats()
				for _, c := range chats {
					if s, ok, _ := db.DHSecret(c.ID); ok {
						mu.Lock()
						secrets = append(secrets, s.Private, s.Public)
						mu.Unlock()
					}
				}
			}
			return nil
		})
		return a, api, db
	}
	alice, aliceAPI, aliceDB := newSide("alice")
	bob, _, _ := newSide("bob")
	if _, err := alice.SendContactRequest("bob"); err != nil {
		t.Fatal(err)
	}
	if err := bob.AcceptContact("alice"); err != nil {
		t.Fatal(err)
	}
	c, err := aliceAPI.CreateChat(context.Background(), "bob", rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 256, Mode: 2, Padding: 3})
	if err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(c.ID[:])
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, _ := bob.GetSnapshot()
		if len(s.Chats) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("чат не появился у bob")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := bob.ConnectChat(id); err != nil {
		t.Fatal(err)
	}
	var key []byte
	for key == nil {
		if k, ok, _ := aliceDB.SessionKey(c.ID); ok {
			key = k
		}
		if time.Now().After(deadline) {
			t.Fatal("ключ не выработан")
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	captured := append([][]byte(nil), secrets...)
	mu.Unlock()
	if len(captured) < 4 {
		t.Fatalf("перехвачено значений: %d", len(captured))
	}
	var views []string
	for _, a := range []*App{alice, bob} {
		snap, _ := a.GetSnapshot()
		opts, _ := a.ChatOptions()
		set, _ := a.GetSettings()
		cv, _ := a.ConnectChat(id)
		for _, v := range []any{snap, opts, set, cv} {
			b, _ := json.Marshal(v)
			views = append(views, string(b))
		}
	}
	mu.Lock()
	views = append(views, events...)
	mu.Unlock()
	needles := []string{}
	for _, v := range append(captured, key) {
		needles = append(needles, hex.EncodeToString(v), strings.ToUpper(hex.EncodeToString(v)), base64.StdEncoding.EncodeToString(v),
			base64.RawURLEncoding.EncodeToString(v))
	}
	for _, text := range views {
		for _, n := range needles {
			if strings.Contains(text, n) {
				t.Fatalf("данные для интерфейса содержат секрет: %.80s…", text)
			}
		}
	}
	if !strings.Contains(strings.Join(views, ""), `"keyState":"READY"`) {
		t.Fatal("в данных нет состояния READY")
	}
}

// Паника при завершении не выходит из shutdown, а записывается в журнал.
func TestShutdownRecovers(t *testing.T) {
	a := testApp(t)
	var buf syncLog
	a.log = log.New(&buf, "", 0)
	a.svc = nil // Close на пустом сервисе паникует
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatal("паника вышла из shutdown")
			}
		}()
		a.shutdown(context.Background())
	}()
	if !strings.Contains(buf.String(), "при завершении") {
		t.Fatalf("паника не записана в журнал: %q", buf.String())
	}
}

// Контекст окна записывается при запуске и читается методами из других горутин без гонки.
func TestContextConcurrentAccess(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a.startup(ctx)
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = a.context()
		}
	}()
	wg.Wait()
	if a.context() != ctx {
		t.Fatal("контекст не записан")
	}
}

// syncLog — журнал, безопасный для записи из горутин.
type syncLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Сбой диалога выбора или сохранения файла приходит в интерфейс ошибкой с кодом, а закрытый диалог —
// пустым результатом без ошибки.
func TestDialogErrors(t *testing.T) {
	a := testApp(t)
	a.ctxMu.Lock()
	a.ctx = context.Background()
	a.ctxMu.Unlock()
	open, save := openFileDialog, saveFileDialog
	t.Cleanup(func() { openFileDialog, saveFileDialog = open, save })
	openFileDialog = func(context.Context, runtime.OpenDialogOptions) (string, error) {
		return "", errors.New("диалог не открылся")
	}
	if m, err := a.SendFile("00"); m != nil || codeOf(t, err) != apperr.FileRead {
		t.Fatalf("сбой диалога выбора: %v %v", m, err)
	}
	openFileDialog = func(context.Context, runtime.OpenDialogOptions) (string, error) { return "", nil }
	if m, err := a.SendFile("00"); m != nil || err != nil {
		t.Fatalf("закрытый диалог выбора: %v %v", m, err)
	}
	if p, err := a.dialogResult("", errors.New("x"), apperr.FileWrite); p != "" || apperr.CodeOf(err) != apperr.FileWrite {
		t.Fatalf("сбой диалога сохранения: %q %v", p, err)
	}
	if p, err := a.dialogResult("", nil, apperr.FileWrite); p != "" || err != nil {
		t.Fatalf("закрытый диалог сохранения: %q %v", p, err)
	}
}
