package main

import (
	"context"
	"fmt"
	"github.com/dgraph-io/ristretto/v2"
	"log"
	"time"

	hub "github.com/haoa-go/cache-hub"
	"github.com/haoa-go/cache-hub/driver"
)

type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// UserCache 是业务缓存类型，真实项目可在这里注入数据库连接。
type UserCache struct{}

func (*UserCache) GetConfig() hub.Config {
	return hub.DefaultConfig("user")
}

func (u *UserCache) GetLevels() []hub.Level {
	return []hub.Level{{Name: "memory", DriverType: hub.TypeOfDriver[*driver.Ristretto](), TTL: time.Minute, NullTTL: 10 * time.Second}}
}
func (*UserCache) Build(ctx context.Context, id int) (*User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 真实项目在这里执行 QueryContext；超时由本方法或调用方控制。
	if id <= 0 {
		return nil, nil
	}
	return &User{ID: id, Name: "示例用户"}, nil
}
func (*UserCache) MultiBuild(ctx context.Context, ids []int) (map[int]*User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 真实项目在这里执行一次 WHERE id IN (...) 查询。
	out := make(map[int]*User, len(ids))
	for _, id := range ids {
		if id > 0 {
			out[id] = &User{ID: id, Name: "批量用户"}
		} else {
			out[id] = nil
		}
	}
	return out, nil
}
func main() {
	local, err := ristretto.NewCache(&ristretto.Config[string, []byte]{NumCounters: 100000, MaxCost: 64 << 20, BufferItems: 64})
	if err != nil {
		log.Fatal("初始化本地缓存失败：", err)
	}
	defer local.Close()
	h, err := hub.NewHubWithConfig(hub.HubConfig{
		Prefix:  "example:",
		Drivers: []hub.Driver{driver.NewRistretto(local)},
	})
	if err != nil {
		log.Fatal("初始化 Hub 失败：", err)
	}
	cache, err := hub.GetCache[int, *User](h, func() *UserCache { return &UserCache{} })
	if err != nil {
		log.Fatal("初始化失败：", err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		local.Wait()
		result, e := cache.Get(ctx, 1)
		if e != nil {
			log.Fatal("查询失败：", e)
		}
		fmt.Printf("数据：%+v，来源：%s\n", result.Data, result.Source)
	}
	batch, e := cache.MultiGet(ctx, []int{1, 2, 3})
	if e != nil {
		log.Fatal("批量查询失败：", e)
	}
	fmt.Printf("批量数据：%+v，来源：%v\n", batch.Data, batch.Source)
}
