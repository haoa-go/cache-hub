# 驱动类型引用的性能评估

驱动通过 `DriverType: cachehub.TypeOfDriver[*driver.Redis]()` 选择，业务不必设置 `Name` 字符串。未指定 `Name` 时，来源名称使用具体 Go 类型名。`Name` 是可选的展示标识，不参与驱动查找。

类型引用使用 `reflect.TypeFor[T]()` 获取类型描述符，Hub 使用 `map[reflect.Type]Driver` 查找实例。注册时使用 `reflect.TypeOf`，层级解析时仍有类型检查及 typed nil 检查。没有通过反射创建对象、遍历字段或调用驱动方法；驱动操作通过普通 Go 接口调用。

当前保留每次操作读取 `GetLevels()` 的语义，未通过缓存层级快照来改变动态配置行为。

## 实测

2026-11-12，Apple M4，Go 1.27.4，darwin/arm64，以下命令运行五轮，结果取中位数：

```sh
go test -run '^$' -bench 'Benchmark(DriverLookup|MemoryHitDriverSelection)$' -benchmem -count=5 -benchtime=300ms .
```

| 场景 | 时间 | 分配次数 |
| --- | ---: | ---: |
| 字符串 map 查找 | 5.19 ns/op | 0 |
| 获取类型描述符并查找类型 map | 7.79 ns/op | 0 |
| 完整内存命中：直接注入驱动 | 1810 ns/op | 40 |
| 完整内存命中：按类型引用驱动 | 1830 ns/op | 40 |

前两项只比较查找操作，不代表旧版完整缓存路径；后两项包含当前库的层级解析、内存驱动及 JSON 解码。完整路径的内存分配均约 2141 B/op。样本较短且未做显著性检验，约 1% 的时间差只能作为量级参考，不应视为稳定的回归比例。这些数据不代表 Redis 网络访问或其他机器上的性能。

当前结果支持保留类型引用：可导航和类型约束的收益明确，额外查找成本为纳秒量级且未增加分配。后续若优化纯内存路径，应对层级解析和序列化分别基准测试，而非直接假设所有反射调用都昂贵。
