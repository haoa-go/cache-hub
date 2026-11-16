package cachehub

import (
	"fmt"
	"reflect"

	"github.com/haoa-go/cache-hub/serializer"
)

// HubConfig 是与应用框架无关的初始化入口。
// 调用方管理连接、日志及依赖的生命周期，库不创建全局单例。
type HubConfig struct {
	Prefix  string
	Lock    Locker
	Logger  Logger
	Drivers []Driver
	// Serializers 保留按名称注册方式，供 SerializerName 引用。
	Serializers map[string]Serializer
	// SerializerInstances 按具体类型注册共享实例，供 SerializerType 引用。
	// 同一类型只能注册一次，实例须支持并发调用；内置注册 *serializer.JSON。
	SerializerInstances []Serializer
	// DefaultSerializer 是已注册序列化器名称，留空时使用 json。
	DefaultSerializer string
}

// NewHubWithConfig 集中配置基础设施，复制驱动列表及序列化器注册表，供多个业务缓存复用。
// 返回后修改传入的列表或 map 不影响 Hub，驱动与序列化器实例本身共享。
// config 为共享驱动、序列化器、锁、日志和前缀配置。
func NewHubWithConfig(config HubConfig) (*Hub, error) {
	h := NewHub(config.Lock, config.Prefix, config.Logger)
	h.drivers = make(map[reflect.Type]Driver, len(config.Drivers))
	for _, d := range config.Drivers {
		if isNil(d) {
			return nil, fmt.Errorf("注册的缓存驱动不能为空")
		}
		typ := reflect.TypeOf(d)
		if _, exists := h.drivers[typ]; exists {
			return nil, fmt.Errorf("缓存驱动类型重复注册：%s", typ)
		}
		h.drivers[typ] = d
	}
	jsonSerializer := &serializer.JSON{}
	h.serializers = map[string]Serializer{"json": jsonSerializer}
	h.serializersByType = map[reflect.Type]Serializer{reflect.TypeOf(jsonSerializer): jsonSerializer}
	registered := make(map[reflect.Type]bool)
	for _, s := range config.SerializerInstances {
		if isNil(s) {
			return nil, fmt.Errorf("注册的序列化器不能为空")
		}
		typ := reflect.TypeOf(s)
		if registered[typ] {
			return nil, fmt.Errorf("序列化器类型重复注册：%s", typ)
		}
		registered[typ] = true
		h.serializersByType[typ] = s
		// 显式注册 JSON 指针时，默认名称也复用该实例。
		if typ == reflect.TypeOf(jsonSerializer) {
			h.serializers["json"] = s
		}
	}
	for name, s := range config.Serializers {
		if name == "" || isNil(s) {
			return nil, fmt.Errorf("注册的序列化器名称或实例无效：%q", name)
		}
		h.serializers[name] = s
	}
	name := config.DefaultSerializer
	if name == "" {
		name = "json"
	}
	var ok bool
	h.defaultSerializer, ok = h.serializers[name]
	if !ok {
		return nil, fmt.Errorf("默认序列化器未注册：%s", name)
	}
	return h, nil
}

func (h *Hub) resolveLevel(level Level) (Level, error) {
	if typ := level.DriverType.typ; typ != nil {
		if level.Driver != nil {
			return level, fmt.Errorf("层级不能同时配置 Driver 和 DriverType：%s", level.Name)
		}
		if typ.Kind() == reflect.Interface {
			return level, fmt.Errorf("驱动引用必须使用具体类型：%s", typ)
		}
		d, ok := h.drivers[typ]
		if !ok {
			return level, fmt.Errorf("缓存驱动类型未注册：%s", typ)
		}
		level.Driver = d
	}
	if isNil(level.Driver) {
		return level, fmt.Errorf("缓存驱动不能为空")
	}
	selections := 0
	if level.Serializer != nil {
		selections++
	}
	if level.SerializerName != "" {
		selections++
	}
	if level.SerializerType.typ != nil {
		selections++
	}
	if selections > 1 {
		return level, fmt.Errorf("层级的 Serializer、SerializerType 和 SerializerName 只能配置一个：%s", level.Name)
	}
	if typ := level.SerializerType.typ; typ != nil {
		if typ.Kind() == reflect.Interface {
			return level, fmt.Errorf("序列化器引用必须使用具体类型：%s", typ)
		}
		s, ok := h.serializersByType[typ]
		if !ok {
			return level, fmt.Errorf("序列化器类型未注册：%s", typ)
		}
		level.Serializer = s
	}
	if level.SerializerName != "" {
		if level.Serializer != nil {
			return level, fmt.Errorf("层级不能同时配置 Serializer 和 SerializerName：%s", level.Name)
		}
		s, ok := h.serializers[level.SerializerName]
		if !ok {
			return level, fmt.Errorf("序列化器未注册：%s", level.SerializerName)
		}
		level.Serializer = s
	}
	if isNil(level.Serializer) {
		level.Serializer = h.defaultSerializer
		if level.Serializer == nil {
			level.Serializer = serializer.JSON{}
		}
	}
	return level, nil
}
