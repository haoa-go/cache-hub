// Package locker 提供与 PHP 相同粒度的构建锁，不自动续租。
package locker

import (
	"context"
	"github.com/redis/go-redis/v9"
	"time"
)

// Redis 按 PHP 语义实现：固定值抢锁、直接删除解锁。
// 不校验所有权，长构建超过租期后可能删除后来者的锁。
type Redis struct{ Client redis.UniversalClient }

func NewRedis(client redis.UniversalClient) *Redis { return &Redis{Client: client} }
func (r *Redis) TryLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return r.Client.SetNX(ctx, key, 1, ttl).Result()
}
func (r *Redis) Unlock(ctx context.Context, key string) (bool, error) {
	n, err := r.Client.Del(ctx, key).Result()
	return n > 0, err
}
func (r *Redis) IsLocked(ctx context.Context, key string) (bool, error) {
	s, err := r.Client.Get(ctx, key).Result()
	if err == redis.Nil {
		return false, nil
	}
	return s != "" && s != "0", err
}
