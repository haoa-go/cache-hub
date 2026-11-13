package cachehub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"time"
)

var (
	ErrLockTimeout              = errors.New("等待构建锁超时")
	ErrMultiBuildNotImplemented = errors.New("业务未实现 MultiBuild")
)

// Cache 的配置属于本对象；Hub 和底层驱动可以复用。
// 调用期间不可并发修改 Config、Hub、CacheDefinition 和层级配置。
type Cache[K comparable, V any] struct {
	Config     Config
	hub        *Hub
	definition CacheDefinition[K, V]
	sourceMu   sync.RWMutex
	source     any
}

// K 为可比较的查询参数类型，V 为缓存数据类型；hub 为共享 Hub，definition 为业务缓存定义。
func New[K comparable, V any](hub *Hub, definition CacheDefinition[K, V]) (*Cache[K, V], error) {
	if hub == nil || isNil(definition) {
		return nil, errors.New("Hub 和业务定义不能为空")
	}
	c := &Cache[K, V]{hub: hub, definition: definition, Config: definition.GetConfig()}
	if _, err := c.levels(); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *Cache[K, V]) GetConfig() *Config { return &c.Config }
func (c *Cache[K, V]) GetSource() any {
	c.sourceMu.RLock()
	defer c.sourceMu.RUnlock()
	if source, ok := c.source.(map[K]string); ok {
		result := make(map[K]string, len(source))
		for k, v := range source {
			result[k] = v
		}
		return result
	}
	return c.source
}
func (c *Cache[K, V]) ClearSource() { c.setSource(nil) }
func (c *Cache[K, V]) setSource(source any) {
	c.sourceMu.Lock()
	if values, ok := source.(map[K]string); ok {
		copy := make(map[K]string, len(values))
		for k, v := range values {
			copy[k] = v
		}
		source = copy
	}
	c.source = source
	c.sourceMu.Unlock()
}
func (c *Cache[K, V]) levels() ([]Level, error) {
	if c.Config.Key == "" {
		return nil, errors.New("缓存 Key 不能为空")
	}
	if c.Config.LockRetryInterval < 0 {
		return nil, errors.New("锁重试间隔不能为负数")
	}
	if c.Config.LockTimeoutMode != LockTimeoutBuild && c.Config.LockTimeoutMode != LockTimeoutError {
		return nil, errors.New("无效的锁超时模式，请使用 DefaultConfig 初始化")
	}
	levels := append([]Level(nil), c.definition.GetLevels()...)
	if len(levels) == 0 {
		return nil, errors.New("缓存层级不能为空")
	}
	seen := make(map[reflect.Type]bool)
	names := make(map[string]bool)
	for i := range levels {
		l := &levels[i]
		resolved, err := c.hub.resolveLevel(*l)
		if err != nil {
			return nil, err
		}
		*l = resolved
		kind := reflect.TypeOf(l.Driver)
		if seen[kind] {
			return nil, errors.New("缓存层级不能配置相同的驱动类型")
		}
		seen[kind] = true
		if l.Name == "" {
			l.Name = kind.String()
		}
		if names[l.Name] {
			return nil, errors.New("缓存层级名称不能重复")
		}
		names[l.Name] = true
		if l.TTL <= 0 {
			l.TTL = 300 * time.Second
		}
		if l.NullTTL <= 0 {
			l.NullTTL = 60 * time.Second
		}
	}
	return levels, nil
}

// MakeKey 与 PHP 一样，将非空参数追加到业务 Key 后；可通过 KeyParams 自定义。
// params 为用于拼接缓存 Key 的查询参数。
func (c *Cache[K, V]) MakeKey(params K) string {
	suffix := ""
	if c.Config.KeyParams != nil {
		suffix = c.Config.KeyParams(params)
	} else {
		suffix = keyParams(params)
	}
	key := c.hub.Prefix + c.Config.Key
	if suffix != "" && suffix != "0" {
		key += ":" + suffix
	}
	return key
}
func keyParams(params any) string {
	if params == nil {
		return ""
	}
	value := reflect.ValueOf(params)
	if value.Kind() == reflect.Array || value.Kind() == reflect.Slice {
		parts := make([]string, value.Len())
		for i := range parts {
			parts[i] = scalarParam(value.Index(i).Interface())
		}
		return strings.Join(parts, "_")
	}
	return scalarParam(params)
}
func scalarParam(value any) string {
	if value == nil {
		return ""
	}
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Bool {
		if v.Bool() {
			return "1"
		}
		return ""
	}
	return fmt.Sprint(value)
}
func emptyData(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return v.IsNil()
	case reflect.Bool:
		return !v.Bool()
	case reflect.String:
		return v.String() == ""
	}
	return false
}
func (c *Cache[K, V]) decode(level Level, b []byte) (V, bool, error) {
	var zero V
	var raw any
	if err := level.Serializer.Decode(b, &raw); err != nil {
		return zero, false, err
	}
	if !emptyData(raw) {
		data := raw
		if c.Config.Versioned {
			m, ok := raw.(map[string]any)
			if !ok || fmt.Sprint(m["cachehub_version"]) != fmt.Sprint(c.Config.Version) {
				return zero, false, nil
			}
			data = m["data"]
		}
		encoded, err := level.Serializer.Encode(data)
		if err != nil {
			return zero, false, err
		}
		var result V
		if err = level.Serializer.Decode(encoded, &result); err != nil {
			return zero, false, err
		}
		return result, true, nil
	}
	sentinel, err := level.Serializer.Encode(c.Config.NullValue)
	if err != nil {
		return zero, false, err
	}
	normalized, err := level.Serializer.Encode(raw)
	if err != nil {
		return zero, false, err
	}
	if bytes.Equal(normalized, sentinel) {
		return zero, true, nil
	}
	return zero, false, nil
}
func (c *Cache[K, V]) save(ctx context.Context, l Level, key string, data V) (bool, error) {
	value := any(data)
	ttl := l.TTL
	if emptyData(data) {
		if !c.Config.CacheNull {
			return true, nil
		}
		value = c.Config.NullValue
		ttl = l.NullTTL
	} else if c.Config.Versioned {
		value = map[string]any{"cachehub_version": c.Config.Version, "data": data}
	}
	b, err := l.Serializer.Encode(value)
	if err != nil {
		return false, err
	}
	return l.Driver.Set(ctx, key, b, ttl)
}
func (c *Cache[K, V]) write(ctx context.Context, l Level, key string, data V) error {
	ok, err := c.save(ctx, l, key, data)
	if err != nil {
		return fmt.Errorf("回填层 %s: %w", l.Name, err)
	}
	if c.hub.Logger != nil {
		if !ok {
			c.hub.Logger.Error(fmt.Sprintf("缓存回填失败，key=%s，层=%s", key, l.Name))
		} else {
			c.hub.Logger.Debug(fmt.Sprintf("缓存回填成功，key=%s，层=%s", key, l.Name))
		}
	}
	return nil
}
func (c *Cache[K, V]) wrap(data V) V {
	if w, ok := c.definition.(Wrapper[V]); ok {
		return w.WrapData(data)
	}
	return data
}

// Get 的 skipLevels 跳过前几层读取，但最终仍回填这些层。
// ctx 为请求上下文，params 为查询参数。skipLevels 为跳过读取的层数，默认 0，最多一个值。
func (c *Cache[K, V]) Get(ctx context.Context, params K, skipLevels ...int) (resultOut Result[V], resultErr error) {
	var zero Result[V]
	levels, err := c.levels()
	if err != nil {
		return zero, err
	}
	start := 0
	if len(skipLevels) > 1 {
		return zero, errors.New("skipLevels 最多传入一个值")
	}
	if len(skipLevels) == 1 {
		start = skipLevels[0]
	}
	if start < 0 {
		return zero, errors.New("skipLevels 不能为负数")
	}
	if start > len(levels) {
		start = len(levels)
	}
	key := c.MakeKey(params)
	pending := append([]Level(nil), levels[:start]...)
	var data V
	hit := false
	source := "build"
	for i := start; i < len(levels); i++ {
		l := levels[i]
		b, found, e := l.Driver.Get(ctx, key)
		if e != nil {
			return zero, e
		}
		if found {
			data, hit, e = c.decode(l, b)
			if e != nil {
				return zero, e
			}
		}
		if hit {
			source = l.Name
			break
		}
		if i == len(levels)-1 && l.Driver.CanLock() {
			var release func() error
			data, hit, release, e = c.lockAndWait(ctx, l, key)
			if release != nil {
				defer func() {
					if unlockErr := release(); unlockErr != nil {
						resultOut = zero
						resultErr = errors.Join(resultErr, unlockErr)
					}
				}()
			}
			if e != nil {
				return zero, e
			}
			if hit {
				source = l.Name
				break
			}
		}
		pending = append(pending, l)
	}
	if !hit {
		data, err = c.definition.Build(ctx, params)
		if err != nil {
			return zero, err
		}
	}
	for i := len(pending) - 1; i >= 0; i-- {
		if err = c.write(ctx, pending[i], key, data); err != nil {
			return zero, err
		}
	}
	result := Result[V]{Data: c.wrap(data), Source: source}
	c.setSource(source)
	return result, nil
}
func (c *Cache[K, V]) lockAndWait(ctx context.Context, l Level, key string) (V, bool, func() error, error) {
	var zero V
	cfg := c.Config
	if !cfg.LockEnabled || cfg.LockRetryCount <= 0 {
		return zero, false, nil, nil
	}
	if c.hub.Lock == nil {
		return zero, false, nil, errors.New("开启构建锁但未配置 Locker")
	}
	lockKey := key + "_lock"
	ttl := time.Duration(math.Round(float64(cfg.LockRetryCount)*cfg.LockRetryInterval.Seconds())+10) * time.Second
	handle, err := c.hub.Lock.TryLock(ctx, lockKey, ttl)
	if err != nil {
		return zero, false, nil, err
	}
	if !isNil(handle) {
		release := func() error {
			// 清理锁不依赖已取消的请求；这不是 Build 的执行期限。
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			_, e := handle.Unlock(cleanup)
			if e != nil {
				return fmt.Errorf("释放缓存锁失败: %w", e)
			}
			return nil
		}
		return zero, false, release, nil
	}
	for i := 0; i < cfg.LockRetryCount; i++ {
		timer := time.NewTimer(cfg.LockRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, false, nil, ctx.Err()
		case <-timer.C:
		}
		b, found, e := l.Driver.Get(ctx, key)
		if e != nil {
			return zero, false, nil, e
		}
		if found {
			data, hit, e := c.decode(l, b)
			if e != nil {
				return zero, false, nil, e
			}
			if hit {
				return data, true, nil, nil
			}
		}
		locked, e := c.hub.Lock.IsLocked(ctx, lockKey)
		if e != nil {
			return zero, false, nil, e
		}
		// 与 PHP 一致：锁消失时放行，不重新抢锁。
		if !locked {
			return zero, false, nil, nil
		}
	}
	if cfg.LockTimeoutMode == LockTimeoutError {
		return zero, false, nil, ErrLockTimeout
	}
	return zero, false, nil, nil
}

// MultiGet 与 PHP 一致：未命中后直接一次 MultiBuild，不参与锁流程。
// ctx 为请求上下文，params 为查询参数列表。
func (c *Cache[K, V]) MultiGet(ctx context.Context, params []K) (MultiResult[K, V], error) {
	var zero MultiResult[K, V]
	levels, err := c.levels()
	if err != nil {
		return zero, err
	}
	remaining := make([]K, 0, len(params))
	seen := make(map[K]bool)
	for _, p := range params {
		if !seen[p] {
			seen[p] = true
			remaining = append(remaining, p)
		}
	}
	result := MultiResult[K, V]{Data: make(map[K]V), Source: make(map[K]string)}
	type pendingWrite struct {
		level  Level
		missed map[string]K
	}
	pending := []pendingWrite{}
	for _, l := range levels {
		if len(remaining) == 0 {
			break
		}
		keys := make([]string, 0, len(remaining))
		mapping := make(map[string]K, len(remaining))
		for _, p := range remaining {
			key := c.MakeKey(p)
			keys = append(keys, key)
			mapping[key] = p
		}
		values, e := l.Driver.MultiGet(ctx, keys)
		if e != nil {
			return zero, e
		}
		next := make([]K, 0)
		missed := make(map[string]K)
		for _, key := range keys {
			p := mapping[key]
			b, found := values[key]
			var value V
			hit := false
			if found {
				value, hit, e = c.decode(l, b)
				if e != nil {
					return zero, e
				}
			}
			if hit {
				result.Data[p] = value
				result.Source[p] = l.Name
			} else {
				next = append(next, p)
				missed[key] = p
			}
		}
		if len(missed) > 0 {
			pending = append(pending, pendingWrite{l, missed})
		}
		remaining = next
	}
	if len(remaining) > 0 {
		builder, ok := c.definition.(MultiBuilder[K, V])
		if !ok {
			return zero, ErrMultiBuildNotImplemented
		}
		built, e := builder.MultiBuild(ctx, remaining)
		if e != nil {
			return zero, e
		}
		for p, value := range built {
			result.Data[p] = value
			result.Source[p] = "build"
		}
	}
	for i := len(pending) - 1; i >= 0; i-- {
		pw := pending[i]
		items := make(map[string][]byte)
		for key, p := range pw.missed {
			value, exists := result.Data[p]
			// PHP 批量只回填非空结果，不写入空值哨兵。
			if !exists || emptyData(value) {
				continue
			}
			var payload any = value
			if c.Config.Versioned {
				payload = map[string]any{"cachehub_version": c.Config.Version, "data": value}
			}
			encoded, e := pw.level.Serializer.Encode(payload)
			if e != nil {
				return zero, e
			}
			items[key] = encoded
		}
		if len(items) > 0 {
			// PHP 批量不检查 multiSet 的 bool，但执行异常仍中断。
			if _, e := pw.level.Driver.MultiSet(ctx, items, pw.level.TTL); e != nil {
				return zero, e
			}
		}
	}
	output := make(map[K]V, len(params))
	for _, p := range params {
		value, exists := result.Data[p]
		if exists && !isNil(value) {
			output[p] = c.wrap(value)
		} else {
			var empty V
			output[p] = empty
		}
	}
	result.Data = output
	c.setSource(result.Source)
	return result, nil
}

// Update 与 PHP 一致，直接 Build，不加锁；返回写入成功的层数。
// ctx 为请求上下文，params 为查询参数。
func (c *Cache[K, V]) Update(ctx context.Context, params K) (int, error) {
	levels, err := c.levels()
	if err != nil {
		return 0, err
	}
	data, err := c.definition.Build(ctx, params)
	if err != nil {
		return 0, err
	}
	key := c.MakeKey(params)
	count := 0
	for i := len(levels) - 1; i >= 0; i-- {
		ok, e := c.save(ctx, levels[i], key, data)
		if e != nil {
			return count, e
		}
		if ok {
			count++
		}
		if c.hub.Logger != nil && !ok {
			c.hub.Logger.Error(fmt.Sprintf("缓存更新失败，key=%s，层=%s", key, levels[i].Name))
		}
	}
	return count, nil
}

// ctx 为请求上下文，params 为查询参数。
func (c *Cache[K, V]) Delete(ctx context.Context, params K) (int, error) {
	levels, err := c.levels()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, l := range levels {
		ok, e := l.Driver.Delete(ctx, c.MakeKey(params))
		if e != nil {
			return count, e
		}
		if ok {
			count++
		}
	}
	return count, nil
}

// ctx 为请求上下文，params 为查询参数列表。
func (c *Cache[K, V]) MultiDelete(ctx context.Context, params []K) (map[string]int, error) {
	levels, err := c.levels()
	if err != nil {
		return nil, err
	}
	result := make(map[string]int, len(levels))
	for _, l := range levels {
		keys := make([]string, 0, len(params))
		seen := make(map[string]bool)
		for _, p := range params {
			key := c.MakeKey(p)
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
		n, e := l.Driver.MultiDelete(ctx, keys)
		if e != nil {
			return result, e
		}
		result[l.Name] = n
	}
	return result, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return v.IsNil()
	}
	return false
}
