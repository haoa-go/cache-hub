package cachehub_test

import (
	"context"
	"strings"
	"testing"

	hub "github.com/haoa-go/cache-hub"
)

func TestDefaultBuildLock(t *testing.T) {
	t.Run("默认开启并释放锁", func(t *testing.T) {
		f := newFixture(t)
		get(t, f.cache, 1)
		if f.lock.attempts.Load() != 1 {
			t.Fatal("默认配置应参与构建锁流程")
		}
		locked, err := f.lock.inner.IsLocked(context.Background(), f.cache.MakeKey(1)+"_lock")
		if err != nil || locked {
			t.Fatal("完成回填后应释放锁", locked, err)
		}
	})
	t.Run("业务可显式关闭", func(t *testing.T) {
		f := newFixture(t)
		cfg := hub.DefaultConfig("unlocked")
		cfg.LockEnabled = false
		c, err := hub.New[int, *user](hub.NewHub(nil, "", nil), configured(f.def, cfg))
		if err != nil {
			t.Fatal(err)
		}
		get(t, c, 1)
		if f.lock.attempts.Load() != 0 {
			t.Fatal("关闭构建锁后不应加锁")
		}
	})
	t.Run("开启时缺少锁实例应报错", func(t *testing.T) {
		f := newFixture(t)
		c, err := hub.New[int, *user](hub.NewHub(nil, "", nil), configured(f.def, hub.DefaultConfig("missing-lock")))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Get(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "未配置 Locker") {
			t.Fatal("不应静默跳过已开启的构建锁", err)
		}
		if f.builds.Load() != 0 {
			t.Fatal("锁配置错误时不应回源")
		}
	})
}
