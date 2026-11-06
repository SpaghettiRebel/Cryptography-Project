package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	apiv1 "cryptochat/server/api/v1"
)

func TestParseFragmentLine(t *testing.T) {
	got, err := parseFragmentLine("  00ff10\r")
	if err != nil || !bytes.Equal(got, []byte{0, 0xFF, 0x10}) {
		t.Fatalf("%x, %v", got, err)
	}
	if got, err := parseFragmentLine("  \r"); got != nil || err != nil {
		t.Fatalf("пустая строка: %x, %v", got, err)
	}
	for _, bad := range []string{"xyz", "abc", "не-hex"} {
		if _, err := parseFragmentLine(bad); err == nil {
			t.Errorf("%q принята", bad)
		}
	}
}

func TestSendFragmentsStopsAtBadLine(t *testing.T) {
	var sent [][]byte
	record := func(f []byte) error {
		sent = append(sent, f)
		return nil
	}
	n, err := sendFragments(strings.NewReader("aa\n\nbb\r\nплохо\ncc\n"), record)
	if err == nil || n != 2 || len(sent) != 2 || !bytes.Equal(sent[1], []byte{0xBB}) {
		t.Fatalf("%d, %v, %x", n, err, sent)
	}
	failure := errors.New("отказ сервера")
	if n, err := sendFragments(strings.NewReader("aa\nbb\n"), func([]byte) error { return failure }); n != 0 || !errors.Is(err, failure) {
		t.Fatalf("ошибка сервера: %d, %v", n, err)
	}
}

func TestSendFragmentsLineLimit(t *testing.T) {
	var got []byte
	line := strings.Repeat("ab", maxFragmentLine/2)
	n, err := sendFragments(strings.NewReader(line+"\n"), func(f []byte) error {
		got = f
		return nil
	})
	if err != nil || n != 1 || len(got) != 61440 {
		t.Fatalf("наибольшая строка: %d, %v, %d", n, err, len(got))
	}
	if _, err := sendFragments(strings.NewReader(strings.Repeat("a", maxFragmentLine+10)), func([]byte) error { return nil }); err == nil {
		t.Fatal("слишком длинная строка принята")
	}
}

// fakeStream отдаёт заранее заданные события; после них ждёт закрытия своей стороны.
type fakeStream struct {
	events chan *apiv1.ReceiveEvent
	closed chan struct{}
	acks   []uint64
	open   []byte
	fail   error
}

func newFakeStream(fragments ...string) *fakeStream {
	s := &fakeStream{events: make(chan *apiv1.ReceiveEvent, len(fragments)+1), closed: make(chan struct{})}
	s.events <- &apiv1.ReceiveEvent{Kind: &apiv1.ReceiveEvent_Opened_{Opened: &apiv1.ReceiveEvent_Opened{}}}
	for i, f := range fragments {
		s.events <- &apiv1.ReceiveEvent{Kind: &apiv1.ReceiveEvent_Fragment{Fragment: &apiv1.FragmentDelivery{DeliveryId: uint64(i + 1), Fragment: []byte(f)}}}
	}
	return s
}

func (s *fakeStream) Send(m *apiv1.ReceiveRequest) error {
	if o := m.GetOpen(); o != nil {
		s.open = o.GetChatId()
	}
	if a := m.GetAck(); a != nil {
		s.acks = append(s.acks, a.GetDeliveryId())
	}
	return nil
}

func (s *fakeStream) Recv() (*apiv1.ReceiveEvent, error) {
	select {
	case ev := <-s.events:
		return ev, nil
	default:
	}
	if s.fail != nil {
		return nil, s.fail
	}
	<-s.closed
	return nil, io.EOF
}

func (s *fakeStream) CloseSend() error {
	close(s.closed)
	return nil
}

func TestReceivePrintsAndAcks(t *testing.T) {
	s := newFakeStream("ab", "cd")
	var out bytes.Buffer
	if err := receiveFragments(s, []byte{7}, 2, time.Minute, &out); err != nil {
		t.Fatal(err)
	}
	want := hex.EncodeToString([]byte("ab")) + "\n" + hex.EncodeToString([]byte("cd")) + "\n"
	if strings.ReplaceAll(out.String(), "\r", "") != want {
		t.Fatalf("вывод %q", out.String())
	}
	if len(s.acks) != 2 || s.acks[0] != 1 || s.acks[1] != 2 || !bytes.Equal(s.open, []byte{7}) {
		t.Fatalf("подтверждения %v, открытие %x", s.acks, s.open)
	}
}

func TestReceiveShortIsAnError(t *testing.T) {
	s := newFakeStream("ab")
	var out bytes.Buffer
	err := receiveFragments(s, []byte{7}, 3, 20*time.Millisecond, &out)
	if err == nil || !strings.Contains(err.Error(), "1 из 3") {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out.String(), hex.EncodeToString([]byte("ab"))) || len(s.acks) != 1 {
		t.Fatalf("вывод %q, подтверждения %v", out.String(), s.acks)
	}
}

func TestReceiveServerError(t *testing.T) {
	s := newFakeStream()
	s.fail = errors.New("отказ сервера")
	if err := receiveFragments(s, []byte{7}, 1, time.Minute, io.Discard); !errors.Is(err, s.fail) {
		t.Fatalf("%v", err)
	}
}
