package locker

import (
	"context"
	"sync"
	"time"
)

// Memory 仅在本进程内互斥，主要用于测试或自定义可加锁驱动。
type Memory struct {
	mu      sync.Mutex
	expires map[string]time.Time
}

func NewMemory() *Memory { return &Memory{expires: make(map[string]time.Time)} }
func (m *Memory) TryLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if until, ok := m.expires[key]; ok && time.Now().Before(until) {
		return false, nil
	}
	m.expires[key] = time.Now().Add(ttl)
	return true, nil
}
func (m *Memory) Unlock(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.expires[key]
	delete(m.expires, key)
	return ok, nil
}
func (m *Memory) IsLocked(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.expires[key]
	if ok && !time.Now().Before(until) {
		delete(m.expires, key)
		return false, nil
	}
	return ok, nil
}
