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

// Config 定义单个业务缓存的键、空值、版本和构建锁策略。
// 建议通过 DefaultConfig 创建后修改所需字段；直接使用零值不等价于默认配置。
// 配置可在独立对象上修改，但并发调用期间不要修改配置和业务对象。
type Config struct {
	// Key 为必填业务缓存键，与 Hub.Prefix 和查询参数共同组成最终缓存键。
	Key string
	// NullValue 是空结果写入缓存时的占位值，DefaultConfig 默认使用空字符串。
	// 应使用 nil、false 或空字符串等可被识别为空值的值；命中后返回数据类型的零值。
	NullValue any
	// CacheNull 控制单条构建的空结果是否缓存，默认 true，过期时间使用层级的 NullTTL。
	// nil、false 和空字符串视为空结果；批量查询始终不回填空结果。
	CacheNull bool
	// Versioned 控制是否包装并校验正常数据的版本号，默认 false；空值占位不包装版本。
	Versioned bool
	// Version 为 Versioned 开启时使用的数据版本号，默认 1。
	// 版本不匹配视为缓存未命中；版本号不参与缓存键拼接。
	Version int
	// LockEnabled 控制普通单条 Get 的回源构建锁，默认 true。
	// 仅最后一级驱动支持锁时生效，需配置 Hub.Lock；批量、Update 和直接 Build 路径不加锁。
	LockEnabled bool
	// LockRetryCount 为未取得锁时最多等待并检查缓存、锁状态的次数，默认 3；小于等于 0 时跳过锁流程。
	LockRetryCount int
	// LockRetryInterval 为每次等待检查前的间隔，默认 100ms，不允许负数。
	// 与重试次数共同决定等待时长和锁租期，不限制 Build 的执行时间。
	LockRetryInterval time.Duration
	// LockTimeoutMode 指定等待次数耗尽后的行为，默认 LockTimeoutBuild，允许直接回源。
	// 设置为 LockTimeoutError 时返回 ErrLockTimeout；锁提前消失时仍允许直接回源。
	LockTimeoutMode LockTimeoutMode
	// KeyParams 自定义查询参数到缓存键后缀的转换，默认 nil，使用内置参数拼接规则。
	// 函数应返回稳定且可区分不同参数的字符串；空字符串或 "0" 不追加到业务键。
	KeyParams func(params any) string
}

// DefaultConfig 返回指定业务键的默认配置；key 不能为空。
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
