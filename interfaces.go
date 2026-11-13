// Package cachehub 提供与 PHP cache-hub 行为对应的多级缓存。
package cachehub

import (
	"context"
	"reflect"
	"sync"
	"time"
)

// Driver 只负责存取字节。bool 区分普通失败或未命中，error 对应 PHP 异常。
// 实现必须支持并发；Get 返回的数据由调用方持有。
// ctx 为请求上下文，key/keys 为缓存 Key，data/items 为字节数据，ttl 为有效期。
type Driver interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, data []byte, ttl time.Duration) (bool, error)
	Delete(ctx context.Context, key string) (bool, error)
	MultiGet(ctx context.Context, keys []string) (map[string][]byte, error)
	MultiSet(ctx context.Context, items map[string][]byte, ttl time.Duration) (bool, error)
	MultiDelete(ctx context.Context, keys []string) (int, error)
	CanLock() bool
}

// Serializer 可按层替换，不绑定 JSON。Decode 的目标参数为指针。
// value 为原始数据，data 为序列化字节数据，target 为解码目标指针。
type Serializer interface {
	Encode(value any) ([]byte, error)
	Decode(data []byte, target any) error
}

// LockHandle 表示某一次成功加锁的所有权，不得用于释放其他请求的锁。
type LockHandle interface {
	// ctx 为清理上下文；已过期或失去所有权时返回 false, nil。
	Unlock(ctx context.Context) (bool, error)
}

// Locker 不自动续租；每次加锁返回独立句柄，未取得锁返回 nil, nil。
type Locker interface {
	// ctx 为请求上下文，key 为锁 Key，ttl 为正数锁有效期。
	TryLock(ctx context.Context, key string, ttl time.Duration) (LockHandle, error)
	// ctx 为请求上下文，key 为锁 Key。
	IsLocked(ctx context.Context, key string) (bool, error)
}

// Logger 的 message 为日志内容。
type Logger interface {
	Debug(message string)
	Error(message string)
}

// DriverType 是驱动的类型引用，不持有连接或全局驱动实例。
// 使用 TypeOfDriver[*driver.Redis]() 创建，可在 IDE 中跳转到实际驱动类型。
type DriverType struct{ typ reflect.Type }

// TypeOfDriver 要求 T 实现 Driver；应传具体类型，而非 Driver 接口。
func TypeOfDriver[T Driver]() DriverType {
	return DriverType{typ: reflect.TypeFor[T]()}
}

type Level struct {
	// Name 是层级及来源标识，同一业务中必须唯一。
	Name string
	// DriverType 按具体 Go 类型引用 Hub 中已注册的驱动。
	DriverType DriverType
	// Driver 保留直接注入方式，与 DriverType 二选一。
	Driver     Driver
	TTL        time.Duration
	NullTTL    time.Duration
	Serializer Serializer
	// SerializerName 选用已注册序列化器，与 Serializer 二选一。
	SerializerName string
}

// CacheDefinition 对应 PHP 业务类；每次操作重新获取层级。
// K 为可比较的查询参数类型，V 为缓存数据类型。
type CacheDefinition[K comparable, V any] interface {
	// GetConfig 在构造时读取一次，配置由业务定义集中声明。
	GetConfig() Config
	GetLevels() []Level
	// ctx 为请求上下文，params 为查询参数。
	Build(ctx context.Context, params K) (V, error)
}

// MultiBuilder 的 K 为查询参数类型，V 为缓存数据类型。
type MultiBuilder[K comparable, V any] interface {
	// ctx 为请求上下文，params 为查询参数列表。
	MultiBuild(ctx context.Context, params []K) (map[K]V, error)
}

// Wrapper 的 V 为缓存数据类型。
type Wrapper[V any] interface {
	// data 为待包装的缓存数据。
	WrapData(data V) V
}

type LockTimeoutMode int

const (
	LockTimeoutBuild LockTimeoutMode = 1
	LockTimeoutError LockTimeoutMode = 2
)

// Config 可在独立对象上修改；并发调用期间不要修改配置和业务对象。
type Config struct {
	Key       string
	NullValue any
	CacheNull bool
	Versioned bool
	Version   int
	// LockEnabled 控制单条回源构建锁，DefaultConfig 默认开启。
	LockEnabled       bool
	LockRetryCount    int
	LockRetryInterval time.Duration
	LockTimeoutMode   LockTimeoutMode
	// KeyParams 的 params 为查询参数，返回缓存键的参数部分。
	KeyParams func(params any) string
}

// key 为业务缓存 Key。
func DefaultConfig(key string) Config {
	return Config{Key: key, NullValue: "", CacheNull: true, LockEnabled: true, Version: 1,
		LockRetryCount: 3, LockRetryInterval: 100 * time.Millisecond, LockTimeoutMode: LockTimeoutBuild}
}

// Hub 共享基础设施和按业务类型缓存的实例。使用后不要按值复制。
// New 总是创建独立对象；GetCache 可复用或替换已保存对象。
type Hub struct {
	Prefix            string
	Lock              Locker
	Logger            Logger
	drivers           map[reflect.Type]Driver
	serializers       map[string]Serializer
	defaultSerializer Serializer
	caches            sync.Map
}

// lock 为构建锁，prefix 为缓存 Key 前缀，logger 为日志；lock 和 logger 可为 nil。
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
