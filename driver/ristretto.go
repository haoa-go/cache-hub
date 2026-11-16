// Package driver 提供 Ristretto 和 Redis 缓存驱动。
package driver

import (
	"context"
	"time"

	"github.com/dgraph-io/ristretto/v2"
	cachehub "github.com/haoa-go/cache-hub"
)

// Ristretto 复用调用方实例；容量配置、Close 和原生操作由调用方管理。
// 写入异步处理，返回 true 仅表示提交成功，仍可能被准入策略拒绝。
type Ristretto struct {
	Client *ristretto.Cache[string, []byte]
}

// client 必须为非 nil；驱动按数据字节数计费，MaxCost 应使用字节作为单位。
func NewRistretto(client *ristretto.Cache[string, []byte]) *Ristretto {
	if client == nil {
		panic("Ristretto 客户端不能为空")
	}
	return &Ristretto{Client: client}
}
func (*Ristretto) CanLock() bool { return false }

// Get 返回字节副本，避免调用方修改共享缓存。
func (m *Ristretto) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	data, ok := m.Client.Get(key)
	return append([]byte(nil), data...), ok, nil
}

// Set 复制输入，保留 Ristretto 的 TTL 和异步准入语义，不主动等待写入。
func (m *Ristretto) Set(ctx context.Context, key string, data []byte, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return m.Client.SetWithTTL(key, append([]byte(nil), data...), int64(len(data)), ttl), nil
}

// Delete 的 bool 表示删除前观测到有效条目；并发写入和过期可能影响统计。
// 无论是否命中都执行 Del，以处理仍在缓冲区中的写入。
func (m *Ristretto) Delete(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, ok := m.Client.Get(key)
	m.Client.Del(key)
	return ok, nil
}

func (m *Ristretto) MultiGet(ctx context.Context, keys []string) (map[string][]byte, error) {
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
func (m *Ristretto) MultiSet(ctx context.Context, items map[string][]byte, ttl time.Duration) (bool, error) {
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
func (m *Ristretto) MultiDelete(ctx context.Context, keys []string) (int, error) {
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

var _ cachehub.Driver = (*Ristretto)(nil)
