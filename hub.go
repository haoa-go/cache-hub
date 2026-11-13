package cachehub

import (
	"fmt"
	"reflect"

	"github.com/haoa-go/cache-hub/serializer"
)

// HubConfig 是与应用框架无关的初始化入口。
// 调用方管理连接、日志及依赖的生命周期，库不创建全局单例。
type HubConfig struct {
	Prefix      string
	Lock        Locker
	Logger      Logger
	Drivers     []Driver
	Serializers map[string]Serializer
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
	h.serializers = map[string]Serializer{"json": serializer.JSON{}}
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
