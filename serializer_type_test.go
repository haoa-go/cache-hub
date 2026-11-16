package cachehub_test

import (
	"context"
	"testing"

	hub "github.com/haoa-go/cache-hub"
	"github.com/haoa-go/cache-hub/serializer"
)

func TestSerializerTypeSharesRegisteredInstance(t *testing.T) {
	f := newFixture(t)
	codec := &prefixedSerializer{prefix: "SHARED:"}
	registrations := []hub.Serializer{codec}
	h, err := hub.NewHubWithConfig(hub.HubConfig{SerializerInstances: registrations})
	if err != nil {
		t.Fatal(err)
	}
	// 修改调用方列表不能替换 Hub 已保存的实例。
	registrations[0] = &prefixedSerializer{prefix: "REPLACED:"}
	f.def.levels = []hub.Level{{Driver: f.local, SerializerType: hub.TypeOfSerializer[*prefixedSerializer]()}}
	a, err := hub.New[int, *user](h, configured(f.def, hub.DefaultConfig("typed")))
	if err != nil {
		t.Fatal(err)
	}
	b, err := hub.New[int, *user](h, configured(f.def, hub.DefaultConfig("typed")))
	if err != nil {
		t.Fatal(err)
	}
	get(t, a, 1)
	if r := get(t, b, 1); r.Source == "build" {
		t.Fatal("未复用共享序列化器")
	}
	// 在没有并发访问时改变实例，确认 Hub 保存的是原实例而非副本。
	codec.prefix = "CHANGED:"
	get(t, a, 2)
	raw, ok, err := f.local.Get(context.Background(), "typed:2")
	if err != nil || !ok || string(raw[:8]) != "CHANGED:" {
		t.Fatal(string(raw), ok, err)
	}
}

func TestSerializerTypeValidationAndBuiltin(t *testing.T) {
	var nilCodec *prefixedSerializer
	for _, codecs := range [][]hub.Serializer{{nilCodec}, {&prefixedSerializer{}, &prefixedSerializer{}}} {
		if _, err := hub.NewHubWithConfig(hub.HubConfig{SerializerInstances: codecs}); err == nil {
			t.Fatal("无效注册应报错")
		}
	}
	h, err := hub.NewHubWithConfig(hub.HubConfig{})
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	for _, level := range []hub.Level{
		{SerializerType: hub.TypeOfSerializer[hub.Serializer]()},
		{SerializerType: hub.TypeOfSerializer[*prefixedSerializer]()},
		{SerializerType: hub.TypeOfSerializer[*serializer.JSON](), Serializer: serializer.JSON{}},
		{SerializerType: hub.TypeOfSerializer[*serializer.JSON](), SerializerName: "json"},
	} {
		level.Driver = f.local
		f.def.levels = []hub.Level{level}
		if _, err := hub.New[int, *user](h, configured(f.def, hub.DefaultConfig("invalid"))); err == nil {
			t.Fatal("无效引用应报错", level)
		}
	}
	f.def.levels = []hub.Level{{Driver: f.local, SerializerType: hub.TypeOfSerializer[*serializer.JSON]()}}
	c, err := hub.New[int, *user](h, configured(f.def, hub.DefaultConfig("builtin")))
	if err != nil {
		t.Fatal(err)
	}
	get(t, c, 1)
	if r := get(t, c, 1); r.Data.ID != 1 || r.Source == "build" {
		t.Fatal(r)
	}
}
