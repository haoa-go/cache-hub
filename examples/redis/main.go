package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
	"time"

	hub "github.com/haoa-go/cache-hub"
	"github.com/haoa-go/cache-hub/driver"
	"github.com/haoa-go/cache-hub/locker"
	"github.com/haoa-go/cache-hub/serializer"
	"github.com/redis/go-redis/v9"
)

type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}
type UserCache struct{}

func (u *UserCache) GetLevels() []hub.Level {
	return []hub.Level{
		{Name: "memory", DriverName: "memory", TTL: 5 * time.Second},
		{Name: "redis", DriverName: "redis", TTL: 300 * time.Second, NullTTL: 60 * time.Second, SerializerName: "json"},
	}
}
func (*UserCache) Build(ctx context.Context, id int) (*User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 替换为业务数据库查询，在业务中自行设置查询超时。
	if id <= 0 {
		return nil, nil
	}
	return &User{id, "示例用户"}, nil
}
func (*UserCache) MultiBuild(ctx context.Context, ids []int) (map[int]*User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 替换为一次数据库 IN 查询；批量路径不加锁。
	result := make(map[int]*User, len(ids))
	for _, id := range ids {
		if id > 0 {
			result[id] = &User{id, "批量用户"}
		} else {
			result[id] = nil
		}
	}
	return result, nil
}

type slogAdapter struct{ logger *slog.Logger }

func (l slogAdapter) Debug(message string) { l.logger.Debug(message) }
func (l slogAdapter) Error(message string) { l.logger.Error(message) }

func main() {
	addr := os.Getenv("CACHEHUB_REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	db := 0
	if configured := os.Getenv("CACHEHUB_REDIS_DATABASE"); configured != "" {
		parsed, e := strconv.Atoi(configured)
		if e != nil || parsed < 0 {
			log.Fatal("无效的 Redis 数据库编号")
		}
		db = parsed
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DB: db, Username: os.Getenv("CACHEHUB_REDIS_USERNAME"), Password: os.Getenv("CACHEHUB_REDIS_PASSWORD")})
	defer client.Close()
	h, err := hub.NewHubWithConfig(hub.HubConfig{
		Prefix: "cachehub:go:example:",
		Lock:   locker.NewRedis(client),
		Logger: slogAdapter{slog.Default()},
		Drivers: map[string]hub.Driver{
			"memory": driver.NewMemory(10000),
			"redis":  driver.NewRedis(client),
		},
		Serializers: map[string]hub.Serializer{"json": serializer.JSON{}},
	})
	if err != nil {
		log.Fatal("初始化 Hub 失败：", err)
	}
	cfg := hub.DefaultConfig("user")
	cfg.LockEnabled = true
	cache, err := hub.GetCache[int, *User](h, func() *UserCache { return &UserCache{} }, cfg)
	if err != nil {
		log.Fatal("初始化失败：", err)
	}
	ctx := context.Background()
	// 跳过内存读取，但仍会回填内存；参数 0 表示正常读取。
	result, err := cache.Get(ctx, 1, 1)
	if err != nil {
		log.Fatal("查询失败：", err)
	}
	fmt.Printf("用户：%+v，来源：%s\n", result.Data, result.Source)
	batch, err := cache.MultiGet(ctx, []int{1, 2, 3})
	if err != nil {
		log.Fatal("批量查询失败：", err)
	}
	fmt.Printf("批量来源：%v\n", batch.Source)
}
