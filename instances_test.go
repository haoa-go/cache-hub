package cachehub_test

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	hub "github.com/haoa-go/cache-hub"
)

// 不同业务类型即使使用相同配置 Key 也必须保存为不同实例。
type anotherDefinition struct{ *definition }

func TestGetCacheReuseAndFreshReplacement(t *testing.T) {
	f := newFixture(t)
	h := hub.NewHub(f.lock, "test:", nil)
	var calls int
	factory := func() *definition { calls++; d := *f.def; return &d }
	config := hub.DefaultConfig("user")
	first, err := hub.GetCache[int, *user](h, configuredFactory(factory, config))
	if err != nil {
		t.Fatal(err)
	}
	// 已存在时不调用工厂，不用新配置覆盖原配置，也不重新验证其层级。
	different := hub.DefaultConfig("ignored")
	reused, err := hub.GetCache[int, *user](h, configuredFactory(factory, different), false)
	if err != nil || reused != first || calls != 1 || reused.Config.Key != "user" {
		t.Fatal(reused, err, calls)
	}
	first.Config.LockRetryCount = 8
	if reused.Config.LockRetryCount != 8 {
		t.Fatal("复用的不是同一个对象")
	}
	fresh, err := hub.GetCache[int, *user](h, configuredFactory(factory, config), true)
	if err != nil || fresh == first || calls != 2 || fresh.Config.LockRetryCount != 3 {
		t.Fatal(fresh, err, calls)
	}
	after, err := hub.GetCache[int, *user](h, configuredFactory(factory, config))
	if err != nil || after != fresh || calls != 2 {
		t.Fatal(after, err, calls)
	}
	// 已发给调用方的旧对象仍独立可用，替换不修改它。
	if first.Config.LockRetryCount != 8 {
		t.Fatal("替换修改了旧对象")
	}
	if r := get(t, after, 1); r.Data.ID != 1 {
		t.Fatal(r)
	}
}

func TestGetCacheSeparatesTypesAndHubs(t *testing.T) {
	f := newFixture(t)
	h := hub.NewHub(nil, "", nil)
	cfg := hub.DefaultConfig("same-key")
	factory := func() *definition { d := *f.def; return &d }
	first, err := hub.GetCache[int, *user](h, configuredFactory(factory, cfg))
	if err != nil {
		t.Fatal(err)
	}
	other, err := hub.GetCache[int, *user](h, configuredFactory(func() *anotherDefinition { return &anotherDefinition{factory()} }, cfg))
	if err != nil || other == first {
		t.Fatal(other, err)
	}
	secondHub, err := hub.GetCache[int, *user](hub.NewHub(nil, "", nil), configuredFactory(factory, cfg))
	if err != nil || secondHub == first {
		t.Fatal(secondHub, err)
	}
	// New 保留不经过实例表的原有语义。
	independent, err := hub.New[int, *user](h, configured(factory(), cfg))
	if err != nil || independent == first {
		t.Fatal(independent, err)
	}
	again, err := hub.GetCache[int, *user](h, configuredFactory(factory, cfg))
	if err != nil || again != first {
		t.Fatal(again, err)
	}
}

func TestGetCacheFailedConstructionDoesNotReplace(t *testing.T) {
	f := newFixture(t)
	h := hub.NewHub(nil, "", nil)
	cfg := hub.DefaultConfig("user")
	factory := func() *definition { d := *f.def; return &d }
	first, err := hub.GetCache[int, *user](h, configuredFactory(factory, cfg))
	if err != nil {
		t.Fatal(err)
	}
	bad := hub.DefaultConfig("")
	if _, err := hub.GetCache[int, *user](h, configuredFactory(factory, bad), true); err == nil {
		t.Fatal("错误配置应失败")
	}
	if _, err := hub.GetCache[int, *user](h, configuredFactory(func() *definition { return nil }, cfg), true); err == nil {
		t.Fatal("空对象应失败")
	}
	reused, err := hub.GetCache[int, *user](h, configuredFactory(factory, cfg))
	if err != nil || reused != first {
		t.Fatal(reused, err)
	}
	// 首次失败也不能污染实例表。
	clean := hub.NewHub(nil, "", nil)
	if _, err := hub.GetCache[int, *user](clean, configuredFactory(factory, bad)); err == nil {
		t.Fatal("错误配置应失败")
	}
	valid, err := hub.GetCache[int, *user](clean, configuredFactory(factory, cfg))
	if err != nil || valid == nil {
		t.Fatal(valid, err)
	}
}

func TestGetCacheConcurrentConstructionDoesNotLockFactory(t *testing.T) {
	f := newFixture(t)
	h := hub.NewHub(nil, "", nil)
	cfg := hub.DefaultConfig("user")
	const n = 16
	var calls atomic.Int32
	allStarted := make(chan struct{})
	factory := func() *definition {
		if calls.Add(1) == n {
			close(allStarted)
		}
		// 首轮构造都能进入工厂；若构造被串行锁住，此测试会超时。
		<-allStarted
		d := *f.def
		return &d
	}
	results := make(chan *hub.Cache[int, *user], n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			cache, err := hub.GetCache[int, *user](h, configuredFactory(factory, cfg))
			results <- cache
			errs <- err
		}()
	}
	var first *hub.Cache[int, *user]
	for i := 0; i < n; i++ {
		instance := <-results
		err := <-errs
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = instance
		}
		if instance != first {
			t.Fatal("首次并发返回了不同的缓存对象")
		}
	}
	if calls.Load() != n {
		t.Fatal("未允许并发重复构造", calls.Load())
	}
}

func TestGetCacheConcurrentFreshReplacement(t *testing.T) {
	f := newFixture(t)
	h := hub.NewHub(nil, "", nil)
	cfg := hub.DefaultConfig("user")
	factory := func() *definition { d := *f.def; return &d }
	first, err := hub.GetCache[int, *user](h, configuredFactory(factory, cfg))
	if err != nil {
		t.Fatal(err)
	}
	const n = 32
	var wg sync.WaitGroup
	created := make(chan *hub.Cache[int, *user], n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fresh, err := hub.GetCache[int, *user](h, configuredFactory(factory, cfg), true)
			if err != nil {
				t.Error(err)
				return
			}
			created <- fresh
			if _, err = hub.GetCache[int, *user](h, configuredFactory(factory, cfg)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(created)
	last, err := hub.GetCache[int, *user](h, configuredFactory(factory, cfg))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	seen := make(map[*hub.Cache[int, *user]]bool)
	for instance := range created {
		if instance == first || seen[instance] {
			t.Fatal("fresh 没有创建独立实例")
		}
		seen[instance] = true
		if instance == last {
			found = true
		}
	}
	if len(seen) != n || !found {
		t.Fatal("最终实例不是 fresh 保存的对象")
	}
}

func TestGetCacheInvalidArguments(t *testing.T) {
	f := newFixture(t)
	h := hub.NewHub(nil, "", nil)
	cfg := hub.DefaultConfig("user")
	factory := func() *definition { return f.def }
	if _, err := hub.GetCache[int, *user](nil, configuredFactory(factory, cfg)); err == nil {
		t.Fatal("空 Hub 应失败")
	}
	if _, err := hub.GetCache[int, *user, *definition](h, nil); err == nil {
		t.Fatal("空工厂应失败")
	}
	if _, err := hub.GetCache[int, *user](h, configuredFactory(factory, cfg), true, false); err == nil {
		t.Fatal("多余 fresh 参数应失败")
	}
	if _, err := hub.GetCache[int, *user](h, configuredFactory(func() hub.CacheDefinition[int, *user] { return f.def }, cfg)); err == nil || !strings.Contains(err.Error(), "具体类型") {
		t.Fatal(err)
	}
}
