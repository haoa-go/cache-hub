// Package cachehub 提供与 PHP cache-hub 行为对应的多级缓存。
package cachehub

import (
	"context"
	"sync"
	"time"
)

// Driver 只负责存取字节。bool 区分普通失败或未命中，error 对应 PHP 异常。
// 实现必须支持并发；Get 返回的数据由调用方持有。
type Driver interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) (bool, error)
	Delete(context.Context, string) (bool, error)
	MultiGet(context.Context, []string) (map[string][]byte, error)
	MultiSet(context.Context, map[string][]byte, time.Duration) (bool, error)
	MultiDelete(context.Context, []string) (int, error)
	CanLock() bool
}

// Serializer 可按层替换，不绑定 JSON。Decode 的目标参数为指针。
type Serializer interface {
	Encode(any) ([]byte, error)
	Decode([]byte, any) error
}

// Locker 与 PHP 一致，不续租；等待时锁消失可直接构建。
type Locker interface {
	TryLock(context.Context, string, time.Duration) (bool, error)
	Unlock(context.Context, string) (bool, error)
	IsLocked(context.Context, string) (bool, error)
}

type Logger interface {
	Debug(string)
	Error(string)
}

type Level struct {
	// Name 是层级及来源标识，同一业务中必须唯一。
	Name string
	// DriverName 从 Hub 中选用已注册驱动，业务缓存无需持有驱动实例。
	DriverName string
	// Driver 保留直接注入方式，与 DriverName 二选一。
	Driver     Driver
	TTL        time.Duration
	NullTTL    time.Duration
	Serializer Serializer
	// SerializerName 选用已注册序列化器，与 Serializer 二选一。
	SerializerName string
}

// Definition 对应 PHP 业务类；每次操作重新获取层级。
type Definition[K comparable, V any] interface {
	GetLevels() []Level
	Build(context.Context, K) (V, error)
}

type MultiBuilder[K comparable, V any] interface {
	MultiBuild(context.Context, []K) (map[K]V, error)
}

type Wrapper[V any] interface {
	WrapData(V) V
}

type LockTimeoutMode int

const (
	LockTimeoutBuild LockTimeoutMode = 1
	LockTimeoutError LockTimeoutMode = 2
)

// Config 可在独立对象上修改；并发调用期间不要修改配置和业务对象。
type Config struct {
	Key               string
	NullValue         any
	CacheNull         bool
	Versioned         bool
	Version           int
	LockEnabled       bool
	LockRetryCount    int
	LockRetryInterval time.Duration
	LockTimeoutMode   LockTimeoutMode
	// KeyParams 可覆盖默认参数编码，返回缓存键的参数部分。
	KeyParams func(any) string
}

func DefaultConfig(key string) Config {
	return Config{Key: key, NullValue: "", CacheNull: true, Version: 1,
		LockRetryCount: 3, LockRetryInterval: 100 * time.Millisecond, LockTimeoutMode: LockTimeoutBuild}
}

// Hub 共享基础设施和按业务类型缓存的实例。使用后不要按值复制。
// New 总是创建独立对象；GetCache 可复用或替换已保存对象。
type Hub struct {
	Prefix            string
	Lock              Locker
	Logger            Logger
	drivers           map[string]Driver
	serializers       map[string]Serializer
	defaultSerializer Serializer
	caches            sync.Map
}

func NewHub(lock Locker, prefix string, logger Logger) *Hub {
	return &Hub{Prefix: prefix, Lock: lock, Logger: logger}
}

type Result[V any] struct {
	Data   V
	Source string
}

type MultiResult[K comparable, V any] struct {
	Data   map[K]V
	Source map[K]string
}
