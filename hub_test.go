package cachehub_test

import (
	"context"
	hub "github.com/haoa-go/cache-hub"
	"github.com/haoa-go/cache-hub/driver"
	"strings"
	"testing"
	"time"
)

func TestHubTypedDriversAndSerializers(t *testing.T) {
	f := newFixture(t)
	f.def.levels = []hub.Level{
		{Name: "memory", DriverType: hub.TypeOfDriver[*driver.Memory](), TTL: time.Minute},
		{Name: "redis", DriverType: hub.TypeOfDriver[*sharedDriver](), TTL: time.Minute, SerializerName: "remote"},
	}
	h, err := hub.NewHubWithConfig(hub.HubConfig{
		Prefix: "registered:", Lock: f.lock,
		Drivers: []hub.Driver{f.local, f.shared},
		Serializers: map[string]hub.Serializer{
			"local":  prefixedSerializer{prefix: "LOCAL:"},
			"remote": prefixedSerializer{prefix: "REMOTE:"},
		}, DefaultSerializer: "local",
	})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := hub.New[int, *user](h, configured(f.def, hub.DefaultConfig("user")))
	if err != nil {
		t.Fatal(err)
	}
	if r := get(t, cache, 1); r.Source != "build" {
		t.Fatal(r)
	}
	if r := get(t, cache, 1); r.Source != "memory" {
		t.Fatal(r)
	}
	if r := get(t, cache, 1, 1); r.Source != "redis" || r.Data.ID != 1 {
		t.Fatal(r)
	}
	local, _, _ := f.local.Get(context.Background(), "registered:user:1")
	remote, _, _ := f.shared.Get(context.Background(), "registered:user:1")
	if !strings.HasPrefix(string(local), "LOCAL:") || !strings.HasPrefix(string(remote), "REMOTE:") {
		t.Fatal(string(local), string(remote))
	}
	// 跳过读取后仍回填，并复用原有 Update / 删除流程。
	if _, err := cache.Update(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if n, err := cache.Delete(context.Background(), 1); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if _, err := cache.MultiGet(context.Background(), []int{1, 2}); err != nil {
		t.Fatal(err)
	}
	if counts, err := cache.MultiDelete(context.Background(), []int{1, 2}); err != nil || counts["memory"] != 2 || counts["redis"] != 2 {
		t.Fatal(counts, err)
	}
}

func TestHubSnapshotsRegistrationsAndSharesInstances(t *testing.T) {
	f := newFixture(t)
	registrations := []hub.Driver{f.local}
	formats := map[string]hub.Serializer{"custom": prefixedSerializer{prefix: "ORIGINAL:"}}
	h, err := hub.NewHubWithConfig(hub.HubConfig{Drivers: registrations, Serializers: formats, DefaultSerializer: "custom"})
	if err != nil {
		t.Fatal(err)
	}
	registrations[0] = driver.NewMemory(1)
	delete(formats, "custom")
	f.def.levels = []hub.Level{{Name: "memory", DriverType: hub.TypeOfDriver[*driver.Memory]()}}
	a, err := hub.New[int, *user](h, configured(f.def, hub.DefaultConfig("user")))
	if err != nil {
		t.Fatal(err)
	}
	b, err := hub.New[int, *user](h, configured(f.def, hub.DefaultConfig("user")))
	if err != nil {
		t.Fatal(err)
	}
	get(t, a, 1)
	if r := get(t, b, 1); r.Source != "memory" || f.builds.Load() != 1 {
		t.Fatal(r, f.builds.Load())
	}
	data, found, err := f.local.Get(context.Background(), "user:1")
	if err != nil || !found || !strings.HasPrefix(string(data), "ORIGINAL:") {
		t.Fatal(string(data), found, err)
	}
}

func TestHubConfigurationErrors(t *testing.T) {
	var nilDriver *driver.Memory
	for _, cfg := range []hub.HubConfig{
		{Drivers: []hub.Driver{driver.NewMemory(1), driver.NewMemory(1)}},
		{Drivers: []hub.Driver{nilDriver}},
		{Serializers: map[string]hub.Serializer{"missing": nil}},
		{DefaultSerializer: "missing"},
	} {
		if _, err := hub.NewHubWithConfig(cfg); err == nil {
			t.Fatal("错误的注册配置应返回错误")
		}
	}
	h, err := hub.NewHubWithConfig(hub.HubConfig{Drivers: []hub.Driver{driver.NewMemory(1)}})
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	for _, level := range []hub.Level{
		{},
		{DriverType: hub.TypeOfDriver[hub.Driver]()},
		{Name: "redis", DriverType: hub.TypeOfDriver[*driver.Redis]()},
		{Name: "memory", DriverType: hub.TypeOfDriver[*driver.Memory](), SerializerName: "missing"},
		{Name: "memory", DriverType: hub.TypeOfDriver[*driver.Memory](), Driver: driver.NewMemory(1)},
		{Name: "memory", DriverType: hub.TypeOfDriver[*driver.Memory](), SerializerName: "json", Serializer: prefixedSerializer{}},
	} {
		f.def.levels = []hub.Level{level}
		if _, err := hub.New[int, *user](h, configured(f.def, hub.DefaultConfig("user"))); err == nil {
			t.Fatal("错误的层级引用应返回错误", level)
		}
	}
}

func TestTypedDriverReferenceIsScopedToHub(t *testing.T) {
	firstDriver, secondDriver := driver.NewMemory(10), driver.NewMemory(10)
	first, err := hub.NewHubWithConfig(hub.HubConfig{Drivers: []hub.Driver{firstDriver}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := hub.NewHubWithConfig(hub.HubConfig{Drivers: []hub.Driver{secondDriver}})
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.def.levels = []hub.Level{{DriverType: hub.TypeOfDriver[*driver.Memory]()}}
	cfg := hub.DefaultConfig("isolated")
	a, err := hub.New[int, *user](first, configured(f.def, cfg))
	if err != nil {
		t.Fatal(err)
	}
	b, err := hub.New[int, *user](second, configured(f.def, cfg))
	if err != nil {
		t.Fatal(err)
	}
	get(t, a, 1)
	if _, found, err := secondDriver.Get(context.Background(), "isolated:1"); err != nil || found {
		t.Fatal("不同 Hub 的同类型驱动不应共享数据", found, err)
	}
	if r := get(t, b, 1); r.Source != "build" {
		t.Fatal("第二个 Hub 应独立回源", r)
	}
	if r := get(t, a, 1); r.Source != "*driver.Memory" {
		t.Fatal("未指定来源名时应使用具体类型名", r)
	}
}
