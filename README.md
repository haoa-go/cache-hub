# cache-hub（Go）

参考 PHP `haoa/cache-hub` 的多级缓存库。业务用独立结构体定义 `GetConfig`、`GetLevels`、`Build` 和可选 `MultiBuild`；库负责分层读取、反向回填和单条构建锁。

要求 Go 1.24 或以上。Redis 连接由外部创建、注入和关闭，库不管理数据库查询。

## 统一初始化入口

库提供与框架无关的 `NewHubWithConfig`，集中注册驱动、序列化器、锁、日志和前缀。应用决定何时创建、是否使用单例、是否交给 DI 管理；库不绑定 xdi、Viper，也不自动读取环境变量。

```go
local, err := ristretto.NewCache(&ristretto.Config[string, []byte]{
    NumCounters: 100000, MaxCost: 64 << 20, BufferItems: 64,
})
if err != nil { return err }
defer local.Close() // 在应用退出、所有缓存操作停止后关闭。

client := redis.NewClient(&redis.Options{
    Addr: "127.0.0.1:6379",
    DB: 11,
    Password: os.Getenv("REDIS_PASSWORD"),
})
// 连接生命周期由应用管理，在应用退出时关闭。

h, err := cachehub.NewHubWithConfig(cachehub.HubConfig{
    Prefix: "myapp:go:",
    Lock: locker.NewRedis(client),
    Logger: appLogger, // 外部日志适配器，也可以传 nil。
    Drivers: []cachehub.Driver{
        driver.NewRistretto(local),
        driver.NewRedis(client),
    },
    Serializers: map[string]cachehub.Serializer{
        "json": serializer.JSON{},
        // 可以注册自定义的其他格式。
    },
    DefaultSerializer: "json",
})
if err != nil {
    return err
}
```

`json` 内置注册，无需重复配置。未指定层级序列化器时使用 `DefaultSerializer`，其默认值为 `json`。注册表在初始化时复制，后续修改原 map 不影响 Hub；驱动实例及连接在各业务间共享。

对应包：`cachehub` 为本模块根包；`driver`、`locker`、`serializer` 为本模块子包；Redis 客户端为 `github.com/redis/go-redis/v9`。

## 业务缓存只声明层级与构建逻辑

业务结构体不需要持有 Ristretto、Redis 或 Serializer 等基础设施字段，按具体驱动类型选择即可。数据库等业务依赖仍可通过自己的结构体字段注入。

```go
type User struct {
    ID int `json:"id"`
    Name string `json:"name"`
}

type UserCache struct{}

func (*UserCache) GetConfig() cachehub.Config {
    cfg := cachehub.DefaultConfig("user")
    cfg.CacheNull = true
    cfg.LockEnabled = true // Hub 需配置 Locker。
    return cfg
}

func (*UserCache) GetLevels() []cachehub.Level {
    return []cachehub.Level{
        {Name: "memory", DriverType: cachehub.TypeOfDriver[*driver.Ristretto](), TTL: 5 * time.Second},
        {Name: "redis", DriverType: cachehub.TypeOfDriver[*driver.Redis](), TTL: 300 * time.Second, NullTTL: 60 * time.Second},
    }
}

func (*UserCache) Build(ctx context.Context, id int) (*User, error) {
    // 替换为业务 QueryContext，查询超时由业务自行控制。
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    if id <= 0 {
        return nil, nil
    }
    return &User{ID: id, Name: "示例用户"}, nil
}

func (*UserCache) MultiBuild(ctx context.Context, ids []int) (map[int]*User, error) {
    // 在这里执行一次 WHERE id IN (...)，不存在的数据可返回 nil。
    if err := ctx.Err(); err != nil {
        return nil, err
    }
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
```

创建并调用：

```go
cache, err := cachehub.New[int, *User](h, &UserCache{})
if err != nil {
    return err
}
result, err := cache.Get(ctx, 123)
if err != nil {
    return err
}
// result.Data == nil 表示不存在；result.Source 为 "memory"、"redis" 或 "build"。
```

层级通过 `DriverType: cachehub.TypeOfDriver[*driver.Redis]()` 引用已注册的具体驱动类型，IDE 可直接跳转到驱动定义。`Name` 只用于返回来源，未设置时使用具体 Go 类型名。未注册的类型、接口类型引用或重复注册同一类型均返回错误。需要单层使用指定格式时配置 `SerializerName`。

保留原有 `NewHub(lock, prefix, logger)` 和层级直接传入 `Driver`、`Serializer` 实例的方式；驱动类型引用与实例二选一，序列化器名称与实例二选一，同时配置会返回错误。`New` 始终返回独立配置对象；新增的 `GetCache` 按业务类型复用实例，支持 `fresh=true` 重建并替换。

完整可运行代码见 `examples/basic` 和 `examples/redis`。DI 接入示例见 [INITIALIZATION.md](INITIALIZATION.md)。

## 按业务类型复用缓存对象

`GetCache` 对应 PHP 的 `getCache(类名, fresh)`：同一个 Hub 内，默认按业务具体类型复用完整 Cache 对象，包括其业务定义和配置。传入工厂函数用于延迟构造，工厂必须返回具体业务类型，例如 `*UserCache`，不能返回 `CacheDefinition` 接口。

```go
newUserCache := func() *UserCache { return &UserCache{} }

// 首次创建并保存；后续默认返回同一对象，不再调用工厂。
cache, err := cachehub.GetCache[int, *User](h, newUserCache)
if err != nil {
    return err
}
same, err := cachehub.GetCache[int, *User](h, newUserCache, false)
// 无错误时 same == cache。

// 创建新对象，并替换 Hub 中该业务类型原来保存的对象。
fresh, err := cachehub.GetCache[int, *User](h, newUserCache, true)
// 无错误时 fresh != cache；下一次默认获取返回 fresh。
```

业务缓存必须实现 `GetConfig() Config`，集中声明 Key、空值缓存、版本、锁和参数编码。`New` 与 `GetCache` 不接收外部业务配置。配置在构造时读取一次并按值保存；复用时不重新读取，需重新读取时传 `fresh=true`。构造失败不缓存失败结果，也不替换已有对象。不同业务类型、不同 Hub 的实例独立；业务数据 Key 不作为对象复用标识，与 PHP 按类名缓存相对应。

Hub 使用 `sync.Map` 保存实例，构造过程不加锁。首次并发获取允许少量重复构造，再用 `LoadOrStore` 返回同一已保存对象；工厂因此不应包含必须只执行一次的副作用。并发 fresh 调用各自创建独立对象，最终保存其中最后完成 Store 的对象。

`fresh=true` 不复制旧对象，旧对象仍可继续使用；驱动和数据库连接可由工厂按需共享。不要在其他 goroutine 正在使用同一个缓存对象时修改它的配置。需要完全绕过实例表、且不替换已保存对象时，仍使用 `New`。Hub 含并发状态，初始化后通过指针共享，不要按值复制。

## 与 PHP 一致的流程

| 操作 | 行为 |
| --- | --- |
| `Get(ctx, id)` | 从第一层向下读取；命中后回填此前未命中的层；全部未命中才 Build |
| `Get(ctx, id, 1)` | 跳过第一层读取，但仍回填第一层 |
| `Get(ctx, id, 层数)` | 直接 Build，并回填全部层，不经过锁 |
| `MultiGet(ctx, ids)` | 逐层批量读取，剩余未命中一次交给 MultiBuild，不加锁 |
| `Update(ctx, id)` | 直接 Build，不加锁；反向写入全部层，返回写入成功层数 |
| `Delete(ctx, id)` | 依次删除所有层，返回成功删除层数 |
| `MultiDelete(ctx, ids)` | 按层批量删除，返回各层删除数量 |

回填顺序从最底层向上。`TTL <= 0` 使用 300 秒，`NullTTL <= 0` 使用 60 秒。

每次操作重新调用业务的 `GetLevels()`；不增加 `WithLevels`。同一业务的层级不允许配置重复的驱动类型，与 PHP 一致。不同业务可复用同一个驱动对象。

`MultiBuild` 未实现时，批量全部命中仍可正常读取；存在未命中时返回 `ErrMultiBuildNotImplemented`。不会自动拆成单条数据库查询。输入重复参数会合并，输出是以参数为键的 map，不保证迭代顺序。

### 空值、版本和包装

单条 Build 的 `nil`、`false` 和空字符串视为空值，默认写入 `NullValue`，使用空值 TTL。数字零和非 nil 的空集合属于正常数据。首次构建返回原始数据；再次命中空值哨兵时，返回类型 `V` 的零值。业务推荐使用 `*User` 等可表示 nil 的类型。

批量与 PHP 一致：只回填非空结果，不缓存空值；遗漏结果不报错，最终输出为对应类型的零值，来源不补入 `build`。

开启 `Versioned` 后，正常值存为 `{"cachehub_version":版本号,"data":数据}`。版本号不加入 Key，版本不匹配视为未命中，空值哨兵不包装版本。

业务可实现 `WrapData(V) V`，在返回前包装数据，缓存中保存未包装值；单条与批量都支持。引用类型的数据如需包装，建议复制后再修改，避免改变业务共享对象。

### 锁

`DefaultConfig` 的默认值：

| 配置 | 默认值 |
| --- | --- |
| `LockEnabled` | `true` |
| `LockRetryCount` | `3` |
| `LockRetryInterval` | `100ms` |
| `LockTimeoutMode` | `LockTimeoutBuild`，即等待超时放行 |
| `CacheNull` | `true` |
| `NullValue` | 空字符串 |
| `Versioned` / `Version` | `false` / `1` |

构建锁默认开启；使用 Redis 等可加锁驱动时，Hub 需配置 `Locker`。业务可在 `GetConfig()` 中设置 `LockEnabled = false` 关闭。只有普通单条 Get 遍历到最后一级，且该驱动 `CanLock()` 为 true 时才参与锁流程。内存驱动为 false，Redis 驱动为 true。设置 `LockRetryCount <= 0` 同 PHP 一样跳过锁流程。

抢到锁后直接 Build，不复查缓存。未抢到锁则按配置等待：每次先查询最后一级缓存，仍未命中则检查锁；锁消失允许直接 Build，不重新抢锁。重试耗尽后，`LockTimeoutBuild` 放行，`LockTimeoutError` 返回 `ErrLockTimeout`。

锁租期为 `round(重试次数 × 间隔秒数) + 10` 秒，不自动续租。每次 Redis 加锁使用独立随机 token；成功后返回 `LockHandle`，解锁通过 Lua 原子校验 token 并删除。过期的旧句柄不能删除新持有者的锁。内存锁同样按独立句柄校验所有权。长构建超过租期以及超时放行仍可能重复回源。

锁会保持到回填结束；Build 或回填返回错误时也会释放。未取得锁的请求不执行解锁。清理使用独立的 1 秒 context，避免请求取消导致锁无法释放；这不限制 Build 的执行时间。解锁执行错误向外返回；已过期或不再拥有锁时返回 false，不视为执行错误。

库不会设置 Build 超时，也不替换传给 Build/MultiBuild 的 context。业务可自行用 `context.WithTimeout` 配合数据库 `QueryContext`。

### 普通失败和执行错误

驱动接口保留两种结果，类似 PHP 的 false 与异常：

```go
Set(context.Context, string, []byte, time.Duration) (bool, error)
```

- 单条回填返回 `false, nil`：记录日志，继续回填，最终返回数据和 nil error。
- 驱动、序列化或 Build 返回 error：立即中断，向调用方返回错误，不提供可用查询结果。
- PHP 批量未检查 `multiSet` 的 bool，本库同样只传播其 error。
- 删除和 Update 在中途出错时可能已有部分层完成操作，不提供事务回滚。

## 序列化、前缀、日志和配置

每层可通过 `SerializerName` 选用已注册的序列化器，也可直接传入 `Serializer` 实例；均未配置时使用 Hub 的默认序列化器（默认 JSON）：

```go
type Serializer interface {
    Encode(any) ([]byte, error)
    Decode([]byte, any) error
}
```

`Decode` 的目标是指针。自定义实现须支持解码到 `*any`，再将业务数据转换到 `*V`；版本包装解码为 `map[string]any`。可以实现其他序列化格式，不必使用 JSON；测试中的自定义带前缀格式可作为最小示例。内置 JSON 保留大整数精度。

Hub 的 `Prefix` 由调用方传入，库不写死 Go 前缀。建议 Go 和 PHP 使用不同前缀，因为 Go 默认 JSON 不兼容 PHP 的原始值/`cachehub_json:` 格式。默认参数拼接遵循 PHP 的常用标量与数组规则：业务 Key + `:` + 参数，多个参数用 `_` 拼接；`0`、`false`、空字符串不追加。复杂参数可通过 `Config.KeyParams` 自定义稳定编码。

日志接口由外部注入，无日志实现时不记录日志：

```go
type Logger interface {
    Debug(string)
    Error(string)
}
```

可适配 slog、Zap 等；`examples/redis` 展示 slog 适配。

每次 `New` 创建独立缓存对象。可修改 `cache.Config` 或通过 `cache.GetConfig()` 修改，不影响其他对象的配置；驱动、连接和业务对象是否共享由调用方决定。开始并发调用后，不要同时修改这些配置或业务层级。

`Get` 返回本次来源，同时保留 `GetSource()` 与 `ClearSource()`。`GetSource()` 返回最近一次成功查询的来源，批量时为 `map[K]string`；并发场景使用本次返回的 `Source`，避免混淆其他请求的来源。

与 PHP 的必要接口区别：Go 不使用动态类加载和原生驱动方法透传；需调用 Redis 原生命令时直接使用外部注入的 client。泛型具体类型无法表示 PHP 所有动态值，空数据推荐使用指针、map 或 slice 表达。

## 测试与示例

```sh
go test ./...
go test -race ./...
go vet ./...
go run ./examples/basic
```

Redis 集成测试默认跳过。手动设置连接信息后运行，密码通过环境变量提供，不写入代码：

```sh
export CACHEHUB_REDIS_ADDR=127.0.0.1:6379
export CACHEHUB_REDIS_DATABASE=11
# 在当前终端安全设置 CACHEHUB_REDIS_PASSWORD。
go test -race -run TestRedisIntegration -v ./...
go run ./examples/redis
```

容器内可将地址改成 `db-redis-1:6379`；宿主机使用其映射端口。集成测试默认数据库为 11，只操作唯一前缀内的已知 Key，测试结束删除这些 Key，不调用 FLUSHDB。

PHP 的优化建议见 `PHP_OPTIMIZATION.md`；本次不修改 PHP。


## 项目级默认配置

Go 通过嵌入复用项目默认配置，无需库提供全局可变配置。业务类型在自己的 `GetConfig()` 中调用 `BaseCache.GetConfig()`，再覆盖业务字段；配置按值返回，各缓存独立。

```go
type BaseCache struct{}

func (BaseCache) GetConfig() cachehub.Config {
    cfg := cachehub.DefaultConfig("")
    cfg.LockEnabled = true
    cfg.CacheNull = true
    return cfg
}

type UserCache struct { BaseCache }

func (c *UserCache) GetConfig() cachehub.Config {
    cfg := c.BaseCache.GetConfig()
    cfg.Key = "user"
    // cfg.LockEnabled = false // 业务可覆盖项目默认值。
    return cfg
}
```

业务仍需提供 `GetLevels()` 和 `Build()`。`BaseCache` 的空 Key 是待业务补充的配置，不能直接用作完整缓存定义。Go 嵌入不提供 PHP 的虚方法继承语义，因此业务重写配置时需显式调用嵌入类型的方法。

## Ristretto 本地驱动

应用创建 `github.com/dgraph-io/ristretto/v2` 的 `*ristretto.Cache[string, []byte]`，通过 `driver.NewRistretto(local)` 注入。驱动不创建或关闭实例，业务层级使用 `TypeOfDriver[*driver.Ristretto]()`，TTL 可设为 1～5 秒。容量由应用配置 `MaxCost`，驱动以 `len(data)` 字节计费，Ristretto 默认还计入条目内部成本；计数器、缓冲等仍需额外内存，预算不是进程内存硬上限。

写入保持异步语义：true 只表示提交成功，不保证准入或立即可读；未命中继续下一层。不要在每次写入后调用 Wait；测试中可使用 Wait 等待队列处理。Get/Set 复制字节，避免跨请求修改。Delete 返回删除前是否观测到有效项，MultiDelete 统计是并发下的近似值。负 TTL 不写入，零 TTL 不过期。
