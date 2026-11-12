package cachehub_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	hub "github.com/haoa-go/cache-hub"
	"github.com/haoa-go/cache-hub/driver"
	"github.com/haoa-go/cache-hub/locker"
	"github.com/redis/go-redis/v9"
)

// 仅操作唯一前缀内的已知 Key，不调用 FLUSHDB 或扫描业务数据。
func TestRedisIntegration(t *testing.T) {
	addr := os.Getenv("CACHEHUB_REDIS_ADDR")
	if addr == "" {
		t.Skip("设置 CACHEHUB_REDIS_ADDR 后运行 Redis 集成测试")
	}
	db := 11
	if configured := os.Getenv("CACHEHUB_REDIS_DATABASE"); configured != "" {
		parsed, e := strconv.Atoi(configured)
		if e != nil || parsed < 0 {
			t.Fatal("无效的 Redis 数据库编号")
		}
		db = parsed
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DB: db, Username: os.Getenv("CACHEHUB_REDIS_USERNAME"), Password: os.Getenv("CACHEHUB_REDIS_PASSWORD")})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal("Redis 连接失败：", err)
	}
	prefix := fmt.Sprintf("cachehub:integration:%d:", time.Now().UnixNano())
	d := driver.NewRedis(client)
	l := locker.NewRedis(client)
	keys := []string{prefix + "user:1", prefix + "user:2", prefix + "user:3", prefix + "user:4", prefix + "user:1_lock", prefix + "lease"}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		client.Del(cleanup, keys...)
	}()
	f := newFixture(t)
	f.def.levels = []hub.Level{{DriverName: "redis", TTL: time.Minute}}
	cfg := hub.DefaultConfig("user")
	cfg.LockEnabled = true
	cfg.Versioned = true
	h, err := hub.NewHubWithConfig(hub.HubConfig{
		Prefix: prefix, Lock: l, Drivers: map[string]hub.Driver{"redis": d},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := hub.New[int, *user](h, f.def, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r, e := c.Get(ctx, 1); e != nil || r.Source != "build" {
		t.Fatal(r, e)
	}
	if r, e := c.Get(ctx, 1); e != nil || r.Source != "redis" || r.Data.ID != 1 {
		t.Fatal(r, e)
	}
	if ttl := client.TTL(ctx, prefix+"user:1").Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatal(ttl)
	}
	if locked, e := l.IsLocked(ctx, prefix+"user:1_lock"); e != nil || locked {
		t.Fatal(locked, e)
	}
	r, err := c.MultiGet(ctx, []int{1, 2, 3})
	if err != nil || r.Source[1] != "redis" || r.Source[2] != "build" {
		t.Fatal(r, err)
	}
	r, err = c.MultiGet(ctx, []int{2, 3})
	if err != nil || r.Source[2] != "redis" {
		t.Fatal(r, err)
	}
	counts, err := c.MultiDelete(ctx, []int{1, 2, 3})
	if err != nil || counts["redis"] != 3 {
		t.Fatal(counts, err)
	}
	f.def.build = func(context.Context, int) (*user, error) { return nil, nil }
	if r, e := c.Get(ctx, 4); e != nil || r.Data != nil {
		t.Fatal(r, e)
	}
	if r, e := c.Get(ctx, 4); e != nil || r.Source != "redis" || r.Data != nil {
		t.Fatal(r, e)
	}
	if ok, e := l.TryLock(ctx, prefix+"lease", 30*time.Millisecond); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if ok, e := l.TryLock(ctx, prefix+"lease", time.Second); e != nil || ok {
		t.Fatal(ok, e)
	}
	time.Sleep(50 * time.Millisecond)
	if ok, e := l.TryLock(ctx, prefix+"lease", time.Second); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if ok, e := l.Unlock(ctx, prefix+"lease"); e != nil || !ok {
		t.Fatal(ok, e)
	}
}
