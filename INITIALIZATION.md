# 在应用中初始化 CacheHub

库的通用入口是 `NewHubWithConfig(HubConfig)`。调用方负责创建 Redis client、日志和其他依赖；业务 Cache 通过 DriverType 选择层级，不关心基础设施如何初始化。

## 普通 Go 项目

应用启动时创建一次 Hub，传给需要构造缓存的业务模块即可。无需 DI 框架，也不要求使用全局变量：

```go
func NewCacheHub(local *ristretto.Cache[string, []byte], client redis.UniversalClient, logger cachehub.Logger) (*cachehub.Hub, error) {
    return cachehub.NewHubWithConfig(cachehub.HubConfig{
        Prefix: "myapp:go:",
        Lock: locker.NewRedis(client),
        Logger: logger,
        Drivers: []cachehub.Driver{
            driver.NewRistretto(local),
            driver.NewRedis(client),
        },
        // 不指定时默认使用内置 json；可注册并选用其他格式。
    })
}
```

这个工厂属于应用，可以读取应用自己的配置，也可以改为接收多个 Redis client，按不同具体驱动类型注册。缓存库不读取 Viper 或环境变量，不创建、关闭传入的连接。

## 使用 xdi 的项目

下面对应应用中 `di/sentry.go` 的注册模式，只是项目接入示例。**库本身不依赖 xdi**，也没有修改 mix-skeleton。

把上面的 `NewCacheHub` 工厂放在应用的 di 包，再在 `di/cachehub.go` 中注册：

```go
package di

import (
    cachehub "github.com/haoa-go/cache-hub"
    "github.com/mix-go/xdi"
)

func init() {
    obj := xdi.Object{
        Name: "cachehub",
        New: func() (interface{}, error) {
            // 项目提供 Redis client；可传入项目自己的日志适配器替换 nil。
            return NewCacheHub(Ristretto(), GoRedis(), nil)
        },
    }
    if err := xdi.Provide(&obj); err != nil {
        panic(err)
    }
}

func CacheHub() (h *cachehub.Hub) {
    if err := xdi.Populate("cachehub", &h); err != nil {
        panic(err)
    }
    return
}
```

`Ristretto()` 和 `GoRedis()` 由应用提供；前者返回应用创建的 Ristretto 实例，应用负责配置容量并在所有操作停止后 Close。工厂中的 driver、locker、redis 类型对应本模块的子包和 go-redis；具体数据库、前缀、日志、生命周期均由项目管理。

业务定义集中配置 Key、空值策略、版本和锁；构造时自动读取：

```go
func (*UserCache) GetConfig() cachehub.Config {
    cfg := cachehub.DefaultConfig("user")
    cfg.CacheNull = true
    return cfg
}
```

业务调用时：

```go
cache, err := cachehub.GetCache[int, *User](di.CacheHub(), func() *UserCache {
    return &UserCache{}
})

// 需要新业务缓存对象并替换已保存实例时传入 true。
fresh, err := cachehub.GetCache[int, *User](di.CacheHub(), func() *UserCache {
    return &UserCache{}
}, true)
```

`UserCache.GetLevels()` 只配置 `DriverType`、TTL 和可选 `SerializerName`，不需要逐个注入 Ristretto 或 Redis。若业务需要数据库，仍由业务自己的构造函数注入。

Go 泛型通过 `cachehub.GetCache` 保留业务参数与返回值类型。Hub 按具体业务类型缓存对象，构造使用应用传入的工厂；不通过类名字符串反射调用构造函数。命中实例表时不会调用工厂，也不会覆盖原配置。`cachehub.New` 仍可始终创建独立对象。

## 按类型复用序列化器

推荐与驱动一样，初始化时注册一次共享实例，业务层仅引用具体类型：

```go
// HubConfig 中配置；SerializerInstances 列表会被复制，实例本身共享。
SerializerInstances: []cachehub.Serializer{&serializer.JSON{}},

// Level 中配置，不创建 JSON 实例。
SerializerType: cachehub.TypeOfSerializer[*serializer.JSON](),
```

未显式注册时，Hub 也内置注册 `*serializer.JSON`。指针类型与值类型不同，引用须与注册类型一致。自定义类型必须注册；同类型重复注册、空实例、接口类型引用和未知类型均报错。共享实例需支持并发使用。

`Serializer`、`SerializerType`、`SerializerName` 三选一，均省略则使用默认序列化器。原有 `Serializers` 名称注册表和 `DefaultSerializer` 名称配置保持兼容；按类型注册使用独立的 `SerializerInstances` 列表，不会自动将名称注册表中的同类型多实例合并。
