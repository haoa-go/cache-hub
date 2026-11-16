package testdriver_test

import (
	"context"
	driver "github.com/haoa-go/cache-hub/internal/testdriver"
	"testing"
	"time"
)

func TestMemoryLRUTTLAndCopy(t *testing.T) {
	ctx := context.Background()
	m := driver.NewMemory(2)
	input := []byte("原始")
	m.Set(ctx, "a", input, time.Minute)
	input[0] = 0
	m.Set(ctx, "b", []byte("b"), time.Minute)
	data, ok, err := m.Get(ctx, "a")
	if err != nil || !ok || string(data) != "原始" {
		t.Fatal(string(data), ok, err)
	}
	data[0] = 0
	data, _, _ = m.Get(ctx, "a")
	if string(data) != "原始" {
		t.Fatal("读取未复制字节")
	}
	m.Set(ctx, "c", []byte("c"), time.Minute)
	if _, ok, _ := m.Get(ctx, "b"); ok {
		t.Fatal("LRU 未淘汰最旧项")
	}
	m.Set(ctx, "short", []byte("x"), time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	if _, ok, _ := m.Get(ctx, "short"); ok {
		t.Fatal("TTL 未生效")
	}
}
