package cachehub_test

import (
	"context"
	"fmt"
	"github.com/haoa-go/cache-hub/internal/testdriver"
	"testing"
	"time"

	hub "github.com/haoa-go/cache-hub"
)

type configuredUserCache struct {
	configCalls int
	buildCalls  int
}

func (d *configuredUserCache) GetConfig() hub.Config {
	d.configCalls++
	cfg := hub.DefaultConfig("custom-user")
	cfg.CacheNull = false
	cfg.KeyParams = func(id any) string { return fmt.Sprintf("id:%v", id) }
	return cfg
}

func (*configuredUserCache) GetLevels() []hub.Level {
	return []hub.Level{{Name: "memory", DriverType: hub.TypeOfDriver[*testdriver.Memory](), TTL: time.Minute}}
}

func (d *configuredUserCache) Build(_ context.Context, id int) (*user, error) {
	d.buildCalls++
	if id <= 0 {
		return nil, nil
	}
	return &user{ID: id, Name: "配置测试用户"}, nil
}

func TestDefinitionOwnsConfigAndTypedCache(t *testing.T) {
	h, err := hub.NewHubWithConfig(hub.HubConfig{
		Prefix: "test:", Drivers: []hub.Driver{testdriver.NewMemory(20)},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := &configuredUserCache{}
	c, err := hub.New[int, *user](h, d)
	if err != nil {
		t.Fatal(err)
	}
	if c.MakeKey(7) != "test:custom-user:id:7" || c.Config.CacheNull {
		t.Fatal("未读取业务定义的配置", c.Config)
	}
	for range 2 {
		result, err := c.Get(context.Background(), 7)
		if err != nil || result.Data == nil || result.Data.ID != 7 {
			t.Fatal(result, err)
		}
	}
	if d.buildCalls != 1 || d.configCalls != 1 {
		t.Fatal("配置应只读取一次，正常数据应命中缓存", d)
	}
	for range 2 {
		if _, err := c.Get(context.Background(), -1); err != nil {
			t.Fatal(err)
		}
	}
	if d.buildCalls != 3 {
		t.Fatal("关闭空值缓存后应每次回源", d.buildCalls)
	}
}

func TestDefinitionConfigOnReuseAndFresh(t *testing.T) {
	// 使用同一内存驱动，业务对象仍由工厂独立构造。
	h, err := hub.NewHubWithConfig(hub.HubConfig{Drivers: []hub.Driver{testdriver.NewMemory(10)}})
	if err != nil {
		t.Fatal(err)
	}
	var definitions []*configuredUserCache
	factory := func() *configuredUserCache {
		d := &configuredUserCache{}
		definitions = append(definitions, d)
		return d
	}
	first, err := hub.GetCache[int, *user](h, factory)
	if err != nil {
		t.Fatal(err)
	}
	same, err := hub.GetCache[int, *user](h, factory)
	if err != nil || same != first || len(definitions) != 1 {
		t.Fatal("复用不应重新创建业务定义", err)
	}
	fresh, err := hub.GetCache[int, *user](h, factory, true)
	if err != nil || fresh == first || len(definitions) != 2 {
		t.Fatal("刷新应重新读取业务配置", err)
	}
	for _, d := range definitions {
		if d.configCalls != 1 {
			t.Fatal("每个新对象应只读取一次配置")
		}
	}
}

func TestNewRejectsTypedNilDefinition(t *testing.T) {
	var d *configuredUserCache
	if _, err := hub.New[int, *user](hub.NewHub(nil, "", nil), d); err == nil {
		t.Fatal("空业务定义应返回错误")
	}
}
