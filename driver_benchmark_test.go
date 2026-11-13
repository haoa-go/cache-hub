package cachehub_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	hub "github.com/haoa-go/cache-hub"
	"github.com/haoa-go/cache-hub/driver"
)

var benchmarkDriver hub.Driver

// 对比驱动选择本身；类型路径包含每次取得类型描述符的成本。
func BenchmarkDriverLookup(b *testing.B) {
	d := driver.NewMemory(10)
	byName := map[string]hub.Driver{"memory": d}
	byType := map[reflect.Type]hub.Driver{reflect.TypeOf(d): d}
	b.Run("字符串", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkDriver = byName["memory"]
		}
	})
	b.Run("类型描述符", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkDriver = byType[reflect.TypeFor[*driver.Memory]()]
		}
	})
}

type benchmarkDefinition struct{ level hub.Level }

func (*benchmarkDefinition) GetConfig() hub.Config    { return hub.DefaultConfig("benchmark") }
func (d *benchmarkDefinition) GetLevels() []hub.Level { return []hub.Level{d.level} }
func (*benchmarkDefinition) Build(context.Context, int) (*user, error) {
	return &user{ID: 1, Name: "基准用户"}, nil
}

// 完整内存命中路径包含层级解析和 JSON 解码，比较类型引用与直接注入。
func BenchmarkMemoryHitDriverSelection(b *testing.B) {
	for _, typed := range []bool{false, true} {
		name := "直接实例"
		if typed {
			name = "类型引用"
		}
		b.Run(name, func(b *testing.B) {
			d := driver.NewMemory(10)
			h, err := hub.NewHubWithConfig(hub.HubConfig{Drivers: []hub.Driver{d}})
			if err != nil {
				b.Fatal(err)
			}
			level := hub.Level{Driver: d, TTL: time.Hour}
			if typed {
				level.Driver = nil
				level.DriverType = hub.TypeOfDriver[*driver.Memory]()
			}
			c, err := hub.New[int, *user](h, &benchmarkDefinition{level: level})
			if err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			if _, err := c.Get(ctx, 1); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := c.Get(ctx, 1); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
