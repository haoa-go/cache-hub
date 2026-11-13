package locker

import (
	"context"
	"fmt"
	"sync"
	"time"

	cachehub "github.com/haoa-go/cache-hub"
)

// Memory 仅在本进程内互斥，主要用于测试或自定义可加锁驱动。
type Memory struct {
	mu    sync.Mutex
	locks map[string]*memoryLock
}

type memoryLock struct {
	owner   *Memory
	key     string
	expires time.Time
}

func NewMemory() *Memory { return &Memory{locks: make(map[string]*memoryLock)} }

// ctx 为请求上下文，key 为锁 Key，ttl 为正数锁有效期。
func (m *Memory) TryLock(ctx context.Context, key string, ttl time.Duration) (cachehub.LockHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("锁有效期必须大于 0")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if held, ok := m.locks[key]; ok && now.Before(held.expires) {
		return nil, nil
	}
	handle := &memoryLock{owner: m, key: key, expires: now.Add(ttl)}
	m.locks[key] = handle
	return handle, nil
}

// ctx 为清理上下文；句柄身份比较与删除在同一临界区内执行。
func (h *memoryLock) Unlock(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	h.owner.mu.Lock()
	defer h.owner.mu.Unlock()
	if h.owner.locks[h.key] != h {
		return false, nil
	}
	delete(h.owner.locks, h.key)
	return time.Now().Before(h.expires), nil
}

// ctx 为请求上下文，key 为锁 Key。
func (m *Memory) IsLocked(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.locks[key]
	if ok && !time.Now().Before(held.expires) {
		delete(m.locks, key)
		return false, nil
	}
	return ok, nil
}

var _ cachehub.Locker = (*Memory)(nil)
var _ cachehub.LockHandle = (*memoryLock)(nil)
