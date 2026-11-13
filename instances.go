package cachehub

import (
	"fmt"
	"reflect"
)

// instanceType 按业务具体类型及泛型签名隔离实例，不按配置 Key 分组。
type instanceType struct {
	definition reflect.Type
	cache      reflect.Type
}

// GetCache 对应 PHP getCache：默认按业务具体类型复用实例；fresh=true 时
// 创建新实例并替换 Hub 中保存的对象。构造失败不影响已保存实例。
//
// factory 应返回具体业务类型（例如 *UserCache），用于在构造前确定类型。
// 命中时不调用 factory，业务定义中的新配置也不会覆盖已有对象的配置。
// 构造过程不加锁，首次并发可能重复构造；LoadOrStore 保证返回同一实例。
// 同一 Hub 必须以指针共享；修改已共享缓存的配置需由调用方避免并发使用。
// K 为可比较的查询参数类型，V 为缓存数据类型，D 为具体业务缓存类型。
// h 为共享 Hub，factory 为业务缓存工厂。
// fresh 为是否重新创建，默认 false。
func GetCache[K comparable, V any, D CacheDefinition[K, V]](h *Hub, factory func() D, fresh ...bool) (*Cache[K, V], error) {
	if h == nil {
		return nil, fmt.Errorf("Hub 不能为空")
	}
	if factory == nil {
		return nil, fmt.Errorf("业务缓存工厂不能为空")
	}
	if len(fresh) > 1 {
		return nil, fmt.Errorf("fresh 最多传入一个值")
	}
	definitionType := reflect.TypeFor[D]()
	if definitionType.Kind() == reflect.Interface {
		return nil, fmt.Errorf("业务缓存工厂必须返回具体类型，不能返回接口：%s", definitionType)
	}
	key := instanceType{definition: definitionType, cache: reflect.TypeFor[*Cache[K, V]]()}
	replace := len(fresh) == 1 && fresh[0]
	if !replace {
		if cached, ok := h.caches.Load(key); ok {
			return cached.(*Cache[K, V]), nil
		}
	}
	definition := factory()
	if isNil(definition) {
		return nil, fmt.Errorf("业务缓存工厂返回了空对象：%s", definitionType)
	}
	instance, err := New[K, V](h, definition)
	if err != nil {
		return nil, err
	}
	if replace {
		h.caches.Store(key, instance)
		return instance, nil
	}
	cached, _ := h.caches.LoadOrStore(key, instance)
	return cached.(*Cache[K, V]), nil
}
