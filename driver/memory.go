// Package driver 提供内存和 Redis 缓存驱动。
package driver

import (
	"container/list"
	"context"
	"sync"
	"time"
)

type memoryEntry struct {
	key     string
	data    []byte
	expires time.Time
}

// Memory 是有容量上限的并发安全 LRU，不启动后台协程。
type Memory struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*list.Element
	order    *list.List
}

// capacity 为最大缓存条目数，小于等于 0 时使用 10000。
func NewMemory(capacity int) *Memory {
	if capacity <= 0 {
		capacity = 10000
	}
	return &Memory{capacity: capacity, entries: make(map[string]*list.Element), order: list.New()}
}
func (*Memory) CanLock() bool { return false }

// ctx 为请求上下文，key 为缓存 Key。
func (m *Memory) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.entries[key]
	if !ok {
		return nil, false, nil
	}
	e := el.Value.(*memoryEntry)
	if !e.expires.IsZero() && !time.Now().Before(e.expires) {
		delete(m.entries, key)
		m.order.Remove(el)
		return nil, false, nil
	}
	m.order.MoveToFront(el)
	return append([]byte(nil), e.data...), true, nil
}

// ctx 为请求上下文，key 为缓存 Key，data 为缓存字节数据，ttl 为有效期。
func (m *Memory) Set(ctx context.Context, key string, data []byte, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	e := &memoryEntry{key: key, data: append([]byte(nil), data...)}
	if ttl > 0 {
		e.expires = time.Now().Add(ttl)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.entries[key]; ok {
		el.Value = e
		m.order.MoveToFront(el)
	} else {
		m.entries[key] = m.order.PushFront(e)
	}
	if m.order.Len() > m.capacity {
		el := m.order.Back()
		delete(m.entries, el.Value.(*memoryEntry).key)
		m.order.Remove(el)
	}
	return true, nil
}

// ctx 为请求上下文，key 为缓存 Key。
func (m *Memory) Delete(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.entries[key]; ok {
		delete(m.entries, key)
		m.order.Remove(el)
		return true, nil
	}
	return false, nil
}

// ctx 为请求上下文，keys 为缓存 Key 列表。
func (m *Memory) MultiGet(ctx context.Context, keys []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(keys))
	for _, key := range keys {
		b, ok, err := m.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if ok {
			out[key] = b
		}
	}
	return out, nil
}

// ctx 为请求上下文，items 为 Key 到字节数据的映射，ttl 为有效期。
func (m *Memory) MultiSet(ctx context.Context, items map[string][]byte, ttl time.Duration) (bool, error) {
	success := true
	for key, data := range items {
		ok, err := m.Set(ctx, key, data, ttl)
		if err != nil {
			return false, err
		}
		success = success && ok
	}
	return success, nil
}

// ctx 为请求上下文，keys 为缓存 Key 列表。
func (m *Memory) MultiDelete(ctx context.Context, keys []string) (int, error) {
	count := 0
	for _, key := range keys {
		ok, err := m.Delete(ctx, key)
		if err != nil {
			return count, err
		}
		if ok {
			count++
		}
	}
	return count, nil
}
