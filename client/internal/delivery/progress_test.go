package delivery

import (
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestProgressRate(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	var got []ProgressEvent
	p := NewProgress(func(e ProgressEvent) { got = append(got, e) }, clk.now)
	// 100 обновлений за 2 с: каждые 20 мс.
	for i := int64(1); i <= 100; i++ {
		p.Update(1, [16]byte{1}, StageSend, i, 100)
		clk.t = clk.t.Add(20 * time.Millisecond)
	}
	if len(got) == 0 || got[0].Done != 1 {
		t.Fatalf("первое значение не отправлено: %+v", got)
	}
	if last := got[len(got)-1]; last.Done != 100 || last.Total != 100 {
		t.Fatalf("последнее значение не отправлено: %+v", last)
	}
	// Не чаще 4 раз в секунду: 2 с дают не больше 8 событий плюс обязательное последнее.
	if len(got) > 9 || len(got) < 7 {
		t.Fatalf("событий %d", len(got))
	}
	// Стадии и сообщения считаются отдельно.
	p.Update(1, [16]byte{1}, StageEncrypt, 5, 10)
	p.Update(2, [16]byte{1}, StageSend, 5, 10)
	if len(got) != 11 {
		t.Fatalf("первые значения других стадий: %d", len(got))
	}
	if d, tot, ok := p.Get(1, StageSend); !ok || d != 100 || tot != 100 {
		t.Fatal("Get")
	}
	p.Forget(1)
	if _, _, ok := p.Get(1, StageSend); ok {
		t.Fatal("Forget")
	}
}

func TestProgressEmpty(t *testing.T) {
	var got []ProgressEvent
	p := NewProgress(func(e ProgressEvent) { got = append(got, e) }, nil)
	p.Update(3, [16]byte{}, StageDecrypt, 0, 0)
	p.Update(3, [16]byte{}, StageDecrypt, 0, 0)
	if len(got) != 1 || got[0].Done != 0 || got[0].Total != 0 {
		t.Fatalf("%+v", got)
	}
}
