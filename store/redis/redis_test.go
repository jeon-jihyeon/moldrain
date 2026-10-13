//go:build integration

package redis_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/jeon-jihyeon/moldrain/persist"
	"github.com/jeon-jihyeon/moldrain/persist/storetest"
	"github.com/jeon-jihyeon/moldrain/store/redis"
)

// Runs against the server at MOLDRAIN_TEST_REDIS
// A server like localhost:6379
func TestContract(t *testing.T) {
	addr := os.Getenv("MOLDRAIN_TEST_REDIS")
	if addr == "" {
		t.Skip("MOLDRAIN_TEST_REDIS is not set")
	}
	c := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = c.Close() })
	run := time.Now().UnixNano()
	n := 0
	storetest.Run(t, func(t *testing.T) persist.Store {
		n++
		prefix := fmt.Sprintf("moldrain-test:%d:%d", run, n)
		t.Cleanup(func() {
			c.Del(context.Background(), "{"+prefix+"}:snapshot", "{"+prefix+"}:changes")
		})
		return redis.New(c, prefix)
	})
}
