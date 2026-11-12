package cachehub_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	hub "github.com/haoa-go/cache-hub"
	"github.com/haoa-go/cache-hub/driver"
	"github.com/haoa-go/cache-hub/locker"
	"github.com/haoa-go/cache-hub/serializer"
)

type user struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}
type definition struct {
	levels []hub.Level
	build  func(context.Context, int) (*user, error)
	multi  func(context.Context, []int) (map[int]*user, error)
	wrap   func(*user) *user
}

func (d *definition) GetLevels() []hub.Level                           { return d.levels }
func (d *definition) Build(ctx context.Context, id int) (*user, error) { return d.build(ctx, id) }
func (d *definition) MultiBuild(ctx context.Context, ids []int) (map[int]*user, error) {
	return d.multi(ctx, ids)
}
func (d *definition) WrapData(u *user) *user {
	if d.wrap != nil {
		return d.wrap(u)
	}
	return u
}

// 第二种驱动模拟 Redis 的可加锁属性，不需要外部服务。
type sharedDriver struct{ *driver.Memory }

func (*sharedDriver) CanLock() bool { return true }

type failedDriver struct {
	*driver.Memory
	setError  error
	setOK     bool
	readError error
}

func (d *failedDriver) Set(ctx context.Context, k string, b []byte, ttl time.Duration) (bool, error) {
	return d.setOK, d.setError
}
func (d *failedDriver) Get(ctx context.Context, k string) ([]byte, bool, error) {
	if d.readError != nil {
		return nil, false, d.readError
	}
	return d.Memory.Get(ctx, k)
}

type logRecorder struct {
	mu       sync.Mutex
	failures int
}

func (*logRecorder) Debug(string)   {}
func (l *logRecorder) Error(string) { l.mu.Lock(); l.failures++; l.mu.Unlock() }

type countedLock struct {
	inner    hub.Locker
	attempts atomic.Int32
	releases atomic.Int32
}

func (l *countedLock) TryLock(ctx context.Context, k string, ttl time.Duration) (bool, error) {
	l.attempts.Add(1)
	return l.inner.TryLock(ctx, k, ttl)
}
func (l *countedLock) Unlock(ctx context.Context, k string) (bool, error) {
	l.releases.Add(1)
	return l.inner.Unlock(ctx, k)
}
func (l *countedLock) IsLocked(ctx context.Context, k string) (bool, error) {
	return l.inner.IsLocked(ctx, k)
}

type fixture struct {
	cache   *hub.Cache[int, *user]
	def     *definition
	local   *driver.Memory
	shared  *sharedDriver
	lock    *countedLock
	builds  atomic.Int32
	batches atomic.Int32
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{local: driver.NewMemory(100), shared: &sharedDriver{driver.NewMemory(100)}, lock: &countedLock{inner: locker.NewMemory()}}
	f.def = &definition{levels: []hub.Level{
		{Name: "memory", Driver: f.local, TTL: time.Minute},
		{Name: "redis", Driver: f.shared, TTL: time.Minute},
	}}
	f.def.build = func(_ context.Context, id int) (*user, error) { f.builds.Add(1); return &user{id, "原始数据"}, nil }
	f.def.multi = func(_ context.Context, ids []int) (map[int]*user, error) {
		f.batches.Add(1)
		result := make(map[int]*user)
		for _, id := range ids {
			result[id] = &user{id, "批量数据"}
		}
		return result, nil
	}
	c, err := hub.New[int, *user](hub.NewHub(f.lock, "test:", nil), f.def, hub.DefaultConfig("user"))
	if err != nil {
		t.Fatal(err)
	}
	f.cache = c
	return f
}
func get(t *testing.T, c *hub.Cache[int, *user], id int, skip ...int) hub.Result[*user] {
	t.Helper()
	r, err := c.Get(context.Background(), id, skip...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestGetLevelsAndSkipWriteBack(t *testing.T) {
	f := newFixture(t)
	if r := get(t, f.cache, 1); r.Source != "build" || r.Data.ID != 1 {
		t.Fatal(r)
	}
	if r := get(t, f.cache, 1); r.Source != "memory" {
		t.Fatal(r)
	}
	if r := get(t, f.cache, 1, 1); r.Source != "redis" {
		t.Fatal(r)
	}
	if r := get(t, f.cache, 1, 2); r.Source != "build" {
		t.Fatal(r)
	}
	if f.builds.Load() != 2 {
		t.Fatal(f.builds.Load())
	}
	if r := get(t, f.cache, 1); r.Source != "memory" {
		t.Fatal(r)
	}
	f.local.Delete(context.Background(), f.cache.MakeKey(1))
	if r := get(t, f.cache, 1); r.Source != "redis" {
		t.Fatal(r)
	}
	if r := get(t, f.cache, 1); r.Source != "memory" {
		t.Fatal(r)
	}
	if f.cache.GetSource() != "memory" {
		t.Fatal(f.cache.GetSource())
	}
	f.cache.ClearSource()
	if f.cache.GetSource() != nil {
		t.Fatal("来源没有清除")
	}
}
func TestTTLAndNullValue(t *testing.T) {
	f := newFixture(t)
	f.def.levels = []hub.Level{{Name: "memory", Driver: f.local, TTL: time.Second, NullTTL: 10 * time.Millisecond}}
	f.def.build = func(context.Context, int) (*user, error) { f.builds.Add(1); return nil, nil }
	if r := get(t, f.cache, 1); r.Data != nil {
		t.Fatal(r)
	}
	if r := get(t, f.cache, 1); r.Source != "memory" || r.Data != nil {
		t.Fatal(r)
	}
	time.Sleep(25 * time.Millisecond)
	if r := get(t, f.cache, 1); r.Source != "build" {
		t.Fatal(r)
	}
	f.cache.Config.CacheNull = false
	get(t, f.cache, 2)
	get(t, f.cache, 2)
	if f.builds.Load() != 4 {
		t.Fatal(f.builds.Load())
	}
}
func TestVersionWrapAndIndependentConfig(t *testing.T) {
	f := newFixture(t)
	f.cache.Config.Versioned = true
	f.def.wrap = func(u *user) *user {
		if u == nil {
			return nil
		}
		copy := *u
		copy.Name += "-包装"
		return &copy
	}
	for i := 0; i < 2; i++ {
		if r := get(t, f.cache, 1); r.Data.Name != "原始数据-包装" {
			t.Fatal(r)
		}
	}
	f.cache.Config.Version = 2
	if r := get(t, f.cache, 1); r.Source != "build" {
		t.Fatal(r)
	}
	b, _, _ := f.shared.Get(context.Background(), "test:user:1")
	if !strings.Contains(string(b), `"cachehub_version":2`) {
		t.Fatal(string(b))
	}
	other, err := hub.New[int, *user](hub.NewHub(f.lock, "test:", nil), f.def, hub.DefaultConfig("user"))
	if err != nil {
		t.Fatal(err)
	}
	other.Config.Version = 99
	if f.cache.Config.Version != 2 {
		t.Fatal("对象配置相互影响")
	}
}
func TestMultiBuildAndMissingBehavior(t *testing.T) {
	f := newFixture(t)
	f.cache.Config.LockEnabled = true
	f.def.multi = func(_ context.Context, ids []int) (map[int]*user, error) {
		f.batches.Add(1)
		if !reflect.DeepEqual(ids, []int{1, 2, 3}) {
			t.Fatalf("批量参数：%v", ids)
		}
		return map[int]*user{1: {1, "批量数据"}, 2: nil}, nil
	}
	f.def.wrap = func(u *user) *user { copy := *u; copy.Name += "-包装"; return &copy }
	r, err := f.cache.MultiGet(context.Background(), []int{1, 2, 3, 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Data) != 3 || r.Data[1].Name != "批量数据-包装" || r.Data[2] != nil || r.Data[3] != nil {
		t.Fatal(r)
	}
	if f.batches.Load() != 1 || f.builds.Load() != 0 || f.lock.attempts.Load() != 0 {
		t.Fatal("批量不应进入单条构建或锁流程")
	}
	for _, id := range []int{2, 3} {
		if _, ok, _ := f.shared.Get(context.Background(), f.cache.MakeKey(id)); ok {
			t.Fatal("批量空值不应回填")
		}
	}
	r, err = f.cache.MultiGet(context.Background(), []int{1})
	if err != nil || r.Source[1] != "memory" || r.Data[1].Name != "批量数据-包装" {
		t.Fatal(r, err)
	}
}
func TestUpdateDeleteAndMultiDelete(t *testing.T) {
	f := newFixture(t)
	f.cache.Config.LockEnabled = true
	n, err := f.cache.Update(context.Background(), 1)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if f.lock.attempts.Load() != 0 {
		t.Fatal("Update 不加锁")
	}
	if r := get(t, f.cache, 1); r.Source != "memory" {
		t.Fatal(r)
	}
	n, err = f.cache.Delete(context.Background(), 1)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	f.cache.Update(context.Background(), 1)
	f.cache.Update(context.Background(), 2)
	counts, err := f.cache.MultiDelete(context.Background(), []int{1, 2})
	if err != nil || counts["memory"] != 2 || counts["redis"] != 2 {
		t.Fatal(counts, err)
	}
}
func TestWriteFalseVersusError(t *testing.T) {
	for _, throw := range []bool{false, true} {
		t.Run(fmt.Sprint(throw), func(t *testing.T) {
			f := newFixture(t)
			log := &logRecorder{}
			d := &failedDriver{Memory: driver.NewMemory(10)}
			if throw {
				d.setError = errors.New("写入执行错误")
			}
			f.def.levels = []hub.Level{{Name: "failed", Driver: d}}
			c, e := hub.New[int, *user](hub.NewHub(nil, "test:", log), f.def, hub.DefaultConfig("user"))
			if e != nil {
				t.Fatal(e)
			}
			result, e := c.Get(context.Background(), 1)
			if throw {
				if e == nil || result.Data != nil {
					t.Fatal(result, e)
				}
			} else if e != nil || result.Data == nil || log.failures != 1 {
				t.Fatal(result, e, log.failures)
			}
		})
	}
}
func TestBuildErrorAndReadError(t *testing.T) {
	f := newFixture(t)
	expected := errors.New("业务错误")
	f.def.build = func(context.Context, int) (*user, error) { return nil, expected }
	if _, err := f.cache.Get(context.Background(), 1); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	if _, ok, _ := f.local.Get(context.Background(), f.cache.MakeKey(1)); ok {
		t.Fatal("构建错误不能写入缓存")
	}
	f.def.levels = []hub.Level{{Driver: &failedDriver{Memory: driver.NewMemory(10), readError: expected}}}
	if _, err := f.cache.Get(context.Background(), 1); !errors.Is(err, expected) {
		t.Fatal(err)
	}
}
func TestLockTimeoutPoliciesAndSkipAll(t *testing.T) {
	for _, mode := range []hub.LockTimeoutMode{hub.LockTimeoutBuild, hub.LockTimeoutError} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := newFixture(t)
			cfg := &f.cache.Config
			cfg.LockEnabled = true
			cfg.LockRetryCount = 2
			cfg.LockRetryInterval = time.Millisecond
			cfg.LockTimeoutMode = mode
			f.lock.inner.TryLock(context.Background(), "test:user:1_lock", time.Second)
			r, err := f.cache.Get(context.Background(), 1)
			if mode == hub.LockTimeoutError {
				if !errors.Is(err, hub.ErrLockTimeout) || f.builds.Load() != 0 {
					t.Fatal(r, err)
				}
			} else if err != nil || r.Data == nil || f.builds.Load() != 1 {
				t.Fatal(r, err)
			}
			// 未持有锁的等待者不能执行解锁。
			if f.lock.releases.Load() != 0 {
				t.Fatal("等待者错误释放锁")
			}
			r, err = f.cache.Get(context.Background(), 2, 2)
			if err != nil || r.Source != "build" || f.lock.attempts.Load() != 1 {
				t.Fatal(r, err)
			}
		})
	}
}
func TestLockDisappearsAllowsBuild(t *testing.T) {
	f := newFixture(t)
	f.cache.Config.LockEnabled = true
	f.cache.Config.LockRetryInterval = time.Millisecond
	f.lock.inner.TryLock(context.Background(), "test:user:1_lock", time.Millisecond)
	r, err := f.cache.Get(context.Background(), 1)
	if err != nil || r.Source != "build" || f.lock.attempts.Load() != 1 || f.lock.releases.Load() != 0 {
		t.Fatal(r, err)
	}
}
func TestConcurrentSingleBuildAndContext(t *testing.T) {
	f := newFixture(t)
	cfg := &f.cache.Config
	cfg.LockEnabled = true
	cfg.LockRetryCount = 100
	cfg.LockRetryInterval = time.Millisecond
	cfg.LockTimeoutMode = hub.LockTimeoutError
	started := make(chan struct{})
	finish := make(chan struct{})
	f.def.build = func(ctx context.Context, id int) (*user, error) {
		if f.builds.Add(1) == 1 {
			close(started)
		}
		select {
		case <-finish:
			return &user{id, "并发数据"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	errs := make(chan error, 21)
	go func() { _, e := f.cache.Get(context.Background(), 1); errs <- e }()
	<-started
	for i := 0; i < 20; i++ {
		go func() {
			r, e := f.cache.Get(context.Background(), 1)
			if e == nil && r.Data.ID != 1 {
				e = errors.New("结果错误")
			}
			errs <- e
		}()
	}
	time.Sleep(10 * time.Millisecond)
	close(finish)
	for i := 0; i < 21; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if f.builds.Load() != 1 {
		t.Fatal(f.builds.Load())
	}
	if locked, _ := f.lock.inner.IsLocked(context.Background(), "test:user:1_lock"); locked {
		t.Fatal("锁未释放")
	}
	f.lock.inner.TryLock(context.Background(), "test:user:2_lock", time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.cache.Get(ctx, 2); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestBuildReceivesCallerContextWithoutLibraryDeadline(t *testing.T) {
	f := newFixture(t)
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "业务上下文")
	f.def.build = func(received context.Context, id int) (*user, error) {
		if received != ctx || received.Value(contextKey{}) != "业务上下文" {
			t.Fatal("上下文未透传")
		}
		if _, ok := received.Deadline(); ok {
			t.Fatal("库添加了构建超时")
		}
		return &user{id, "成功"}, nil
	}
	if _, err := f.cache.Get(ctx, 1); err != nil {
		t.Fatal(err)
	}
}

// 自定义格式验证 Serializer 不固定为 JSON，也可以逐层配置。
type prefixedSerializer struct {
	serializer.JSON
	prefix string
}

func (s prefixedSerializer) Encode(v any) ([]byte, error) {
	b, e := s.JSON.Encode(v)
	return append([]byte(s.prefix), b...), e
}
func (s prefixedSerializer) Decode(b []byte, v any) error {
	if !strings.HasPrefix(string(b), s.prefix) {
		return errors.New("格式前缀错误")
	}
	return s.JSON.Decode(b[len(s.prefix):], v)
}
func TestPerLevelSerializerAndPrefix(t *testing.T) {
	f := newFixture(t)
	f.def.levels[0].Serializer = prefixedSerializer{prefix: "L1:"}
	f.def.levels[1].Serializer = prefixedSerializer{prefix: "L2:"}
	c, e := hub.New[int, *user](hub.NewHub(nil, "custom:", nil), f.def, hub.DefaultConfig("user"))
	if e != nil {
		t.Fatal(e)
	}
	get(t, c, 1)
	b, _, _ := f.local.Get(context.Background(), "custom:user:1")
	if !strings.HasPrefix(string(b), "L1:") {
		t.Fatal(string(b))
	}
	b, _, _ = f.shared.Get(context.Background(), "custom:user:1")
	if !strings.HasPrefix(string(b), "L2:") {
		t.Fatal(string(b))
	}
	if r := get(t, c, 1, 1); r.Source != "redis" || r.Data.ID != 1 {
		t.Fatal(r)
	}
}

type scalarDefinition struct {
	level hub.Level
	value any
	count int
}

func (d *scalarDefinition) GetLevels() []hub.Level { return []hub.Level{d.level} }
func (d *scalarDefinition) Build(context.Context, string) (any, error) {
	d.count++
	return d.value, nil
}
func TestPHPEmptySemantics(t *testing.T) {
	for _, value := range []any{false, "", nil, 0, []any{}, map[string]any{}} {
		t.Run(fmt.Sprintf("%T_%v", value, value), func(t *testing.T) {
			d := &scalarDefinition{level: hub.Level{Driver: driver.NewMemory(10)}, value: value}
			c, e := hub.New[string, any](hub.NewHub(nil, "", nil), d, hub.DefaultConfig("scalar"))
			if e != nil {
				t.Fatal(e)
			}
			first, e := c.Get(context.Background(), "key")
			if e != nil || !reflect.DeepEqual(first.Data, value) {
				t.Fatal(first, e)
			}
			second, e := c.Get(context.Background(), "key")
			if e != nil || d.count != 1 {
				t.Fatal(second, e, d.count)
			}
			if value == nil || reflect.TypeOf(value).Kind() == reflect.Bool || reflect.TypeOf(value).Kind() == reflect.String {
				if second.Data != nil {
					t.Fatal("空值命中应返回 nil", second)
				}
			} else if second.Data == nil {
				t.Fatal("零或空集合不能当作空值")
			}
		})
	}
}
func TestValidation(t *testing.T) {
	f := newFixture(t)
	f.def.levels = []hub.Level{{Driver: f.local}, {Driver: driver.NewMemory(1)}}
	if _, err := f.cache.Get(context.Background(), 1); err == nil {
		t.Fatal("重复驱动应该报错")
	}
	f.def.levels = nil
	if _, err := f.cache.Get(context.Background(), 1); err == nil {
		t.Fatal("空层级应该报错")
	}
}

func TestLargeIntegerRoundTrip(t *testing.T) {
	f := newFixture(t)
	id := int(9007199254740993)
	get(t, f.cache, id)
	if r := get(t, f.cache, id); r.Data.ID != id {
		t.Fatal("大整数精度丢失", r.Data.ID)
	}
}
func TestDefaultConfiguration(t *testing.T) {
	cfg := hub.DefaultConfig("user")
	if cfg.LockEnabled || cfg.LockTimeoutMode != hub.LockTimeoutBuild || cfg.LockRetryCount != 3 || cfg.LockRetryInterval != 100*time.Millisecond || !cfg.CacheNull || cfg.Versioned || cfg.Version != 1 || cfg.NullValue != "" {
		t.Fatal(cfg)
	}
}
