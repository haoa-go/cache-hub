// Package locker 提供具有所有权校验的构建锁，不自动续租。
package locker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	cachehub "github.com/haoa-go/cache-hub"
	"github.com/redis/go-redis/v9"
)

// Redis 复用调用方连接，每次成功加锁返回独立所有权句柄。
type Redis struct{ Client redis.UniversalClient }

type redisLock struct {
	client redis.UniversalClient
	key    string
	token  string
}

var unlockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0
`)

// client 为调用方管理的 Redis 客户端。
func NewRedis(client redis.UniversalClient) *Redis { return &Redis{Client: client} }

// ctx 为请求上下文，key 为锁 Key，ttl 为正数锁有效期。
func (r *Redis) TryLock(ctx context.Context, key string, ttl time.Duration) (cachehub.LockHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("锁有效期必须大于 0")
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	handle := &redisLock{client: r.Client, key: key, token: hex.EncodeToString(token[:])}
	acquired, err := r.Client.SetNX(ctx, key, handle.token, ttl).Result()
	if err != nil || !acquired {
		return nil, err
	}
	return handle, nil
}

// ctx 为清理上下文；比较 token 和删除在 Redis 中原子执行。
func (h *redisLock) Unlock(ctx context.Context) (bool, error) {
	n, err := unlockScript.Run(ctx, h.client, []string{h.key}, h.token).Int64()
	return n > 0, err
}

// ctx 为请求上下文，key 为锁 Key。
func (r *Redis) IsLocked(ctx context.Context, key string) (bool, error) {
	n, err := r.Client.Exists(ctx, key).Result()
	return n > 0, err
}

var _ cachehub.Locker = (*Redis)(nil)
var _ cachehub.LockHandle = (*redisLock)(nil)
