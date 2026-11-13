package locker

import (
	"context"
	"sync"
	"testing"
	"time"

	cachehub "github.com/haoa-go/cache-hub"
)

func TestMemoryLockOwnership(t *testing.T) {
	ctx := context.Background()
	locks := NewMemory()
	old, err := locks.TryLock(ctx, "key", time.Minute)
	if err != nil || old == nil {
		t.Fatal("首次加锁失败", err)
	}
	if other, err := locks.TryLock(ctx, "key", time.Minute); err != nil || other != nil {
		t.Fatal("不应重复取得锁", err)
	}
	// 直接推进租期，避免依赖真实等待或调度时序。
	locks.mu.Lock()
	old.(*memoryLock).expires = time.Now().Add(-time.Second)
	locks.mu.Unlock()
	current, err := locks.TryLock(ctx, "key", time.Minute)
	if err != nil || current == nil {
		t.Fatal("过期后应能加锁", err)
	}
	if ok, err := old.Unlock(ctx); err != nil || ok {
		t.Fatal("旧句柄不能释放新锁", ok, err)
	}
	if held, err := locks.IsLocked(ctx, "key"); err != nil || !held {
		t.Fatal("新锁被误删", held, err)
	}
	if ok, err := current.Unlock(ctx); err != nil || !ok {
		t.Fatal("持有者应能释放锁", ok, err)
	}
	if ok, err := current.Unlock(ctx); err != nil || ok {
		t.Fatal("重复解锁应无操作", ok, err)
	}
	third, err := locks.TryLock(ctx, "key", time.Minute)
	if err != nil || third == nil {
		t.Fatal("释放后应能加锁", err)
	}
	if ok, err := current.Unlock(ctx); err != nil || ok {
		t.Fatal("已释放的句柄不能删除后续锁", ok, err)
	}
	if ok, err := third.Unlock(ctx); err != nil || !ok {
		t.Fatal("后续锁所有权应保留", ok, err)
	}
}

func TestMemoryLockCancellationAndExpiration(t *testing.T) {
	locks := NewMemory()
	ctx := context.Background()
	handle, err := locks.TryLock(ctx, "key", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if ok, err := handle.Unlock(canceled); err == nil || ok {
		t.Fatal("取消的上下文不应执行解锁")
	}
	if held, err := locks.IsLocked(ctx, "key"); err != nil || !held {
		t.Fatal("取消的解锁不应删除锁")
	}
	locks.mu.Lock()
	handle.(*memoryLock).expires = time.Now().Add(-time.Second)
	locks.mu.Unlock()
	if ok, err := handle.Unlock(ctx); err != nil || ok {
		t.Fatal("过期锁应返回 false", ok, err)
	}
	if handle, err := locks.TryLock(canceled, "other", time.Minute); err == nil || handle != nil {
		t.Fatal("取消的请求不能加锁")
	}
	for _, ttl := range []time.Duration{0, -time.Second} {
		if h, err := locks.TryLock(ctx, "invalid", ttl); err == nil || h != nil {
			t.Fatal("无效租期不能加锁")
		}
		if h, err := NewRedis(nil).TryLock(ctx, "invalid", ttl); err == nil || h != nil {
			t.Fatal("Redis 无效租期不能加锁")
		}
	}
}

func TestMemoryConcurrentLockOwnership(t *testing.T) {
	locks := NewMemory()
	ctx := context.Background()
	var wg sync.WaitGroup
	acquired := make(chan cachehub.LockHandle, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := locks.TryLock(ctx, "shared", time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			if h != nil {
				acquired <- h
			}
		}()
	}
	wg.Wait()
	close(acquired)
	if len(acquired) != 1 {
		t.Fatal("并发竞争只能有一个持有者", len(acquired))
	}
	if ok, err := (<-acquired).Unlock(ctx); err != nil || !ok {
		t.Fatal("持有者解锁失败", ok, err)
	}
}
