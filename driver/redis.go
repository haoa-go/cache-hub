package driver

import (
	"context"
	"errors"
	"github.com/redis/go-redis/v9"
	"time"
)

// Redis 复用调用方连接，连接生命周期由调用方管理。
type Redis struct{ Client redis.UniversalClient }

// client 为调用方管理的 Redis 客户端。
func NewRedis(client redis.UniversalClient) *Redis { return &Redis{Client: client} }
func (*Redis) CanLock() bool                       { return true }

// ctx 为请求上下文，key 为缓存 Key。
func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool, error) {
	b, err := r.Client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	return b, err == nil, err
}

// ctx 为请求上下文，key 为缓存 Key，data 为缓存字节数据，ttl 为有效期。
func (r *Redis) Set(ctx context.Context, key string, data []byte, ttl time.Duration) (bool, error) {
	err := r.Client.Set(ctx, key, data, ttl).Err()
	return err == nil, err
}

// ctx 为请求上下文，key 为缓存 Key。
func (r *Redis) Delete(ctx context.Context, key string) (bool, error) {
	n, err := r.Client.Del(ctx, key).Result()
	return n > 0, err
}

// ctx 为请求上下文，keys 为缓存 Key 列表。
func (r *Redis) MultiGet(ctx context.Context, keys []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	// 每个 key 独立 GET，流水线也适用于跨 slot 的 Redis Cluster。
	pipe := r.Client.Pipeline()
	cmds := make([]*redis.StringCmd, len(keys))
	for i, key := range keys {
		cmds[i] = pipe.Get(ctx, key)
	}
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	for i, cmd := range cmds {
		b, e := cmd.Bytes()
		if errors.Is(e, redis.Nil) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out[keys[i]] = b
	}
	return out, nil
}

// ctx 为请求上下文，items 为 Key 到字节数据的映射，ttl 为有效期。
func (r *Redis) MultiSet(ctx context.Context, items map[string][]byte, ttl time.Duration) (bool, error) {
	if len(items) == 0 {
		return true, nil
	}
	pipe := r.Client.Pipeline()
	for key, data := range items {
		pipe.Set(ctx, key, data, ttl)
	}
	_, err := pipe.Exec(ctx)
	return err == nil, err
}

// ctx 为请求上下文，keys 为缓存 Key 列表。
func (r *Redis) MultiDelete(ctx context.Context, keys []string) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	pipe := r.Client.Pipeline()
	cmds := make([]*redis.IntCmd, len(keys))
	for i, key := range keys {
		cmds[i] = pipe.Del(ctx, key)
	}
	_, err := pipe.Exec(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, cmd := range cmds {
		count += int(cmd.Val())
	}
	return count, nil
}
