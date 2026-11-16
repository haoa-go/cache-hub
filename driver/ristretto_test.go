package driver_test

import (
	"context"
	"testing"
	"time"

	"github.com/dgraph-io/ristretto/v2"
	"github.com/haoa-go/cache-hub/driver"
)

func TestRistrettoTTLAndCopy(t *testing.T) {
	client, err := ristretto.NewCache(&ristretto.Config[string, []byte]{NumCounters: 1000, MaxCost: 1 << 20, BufferItems: 64})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	d := driver.NewRistretto(client)
	ctx := context.Background()
	input := []byte("原始")
	if ok, err := d.Set(ctx, "a", input, time.Minute); err != nil || !ok {
		t.Fatal(ok, err)
	}
	input[0] = 0
	client.Wait()
	data, ok, err := d.Get(ctx, "a")
	if err != nil || !ok || string(data) != "原始" {
		t.Fatal(string(data), ok, err)
	}
	data[0] = 0
	data, _, _ = d.Get(ctx, "a")
	if string(data) != "原始" {
		t.Fatal("读取未复制字节")
	}
	if ok, err := d.Set(ctx, "short", []byte("x"), 20*time.Millisecond); err != nil || !ok {
		t.Fatal(ok, err)
	}
	client.Wait()
	time.Sleep(40 * time.Millisecond)
	if _, ok, err := d.Get(ctx, "short"); ok || err != nil {
		t.Fatal("TTL 未生效", err)
	}
	if ok, err := d.Set(ctx, "negative", []byte("x"), -time.Second); ok || err != nil {
		t.Fatal("负 TTL 不应写入", err)
	}
	if ok, err := d.MultiSet(ctx, map[string][]byte{"b": []byte("b"), "c": []byte("c")}, time.Minute); err != nil || !ok {
		t.Fatal(ok, err)
	}
	client.Wait()
	items, err := d.MultiGet(ctx, []string{"b", "c", "missing"})
	if err != nil || len(items) != 2 {
		t.Fatal(items, err)
	}
	n, err := d.MultiDelete(ctx, []string{"b", "b", "c", "missing"})
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	client.Wait()
	if _, ok, _ := d.Get(ctx, "b"); ok {
		t.Fatal("删除失败")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if ok, err := d.Set(canceled, "cancel", nil, 0); ok || err != context.Canceled {
		t.Fatal(ok, err)
	}
	if _, _, err := d.Get(canceled, "a"); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestRistrettoRejectsOversizedItem(t *testing.T) {
	client, err := ristretto.NewCache(&ristretto.Config[string, []byte]{NumCounters: 100, MaxCost: 64, BufferItems: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	d := driver.NewRistretto(client)
	d.Set(context.Background(), "large", make([]byte, 1024), time.Minute)
	client.Wait()
	if _, ok, _ := d.Get(context.Background(), "large"); ok {
		t.Fatal("超预算条目不应被接纳")
	}
}
