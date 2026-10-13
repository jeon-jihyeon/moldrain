//go:build integration

package kafka_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/jeon-jihyeon/moldrain/persist"
	"github.com/jeon-jihyeon/moldrain/persist/storetest"
	"github.com/jeon-jihyeon/moldrain/store/kafka"
)

// Runs against the brokers at MOLDRAIN_TEST_KAFKA
// Brokers like localhost:9092
func TestContract(t *testing.T) {
	brokers := os.Getenv("MOLDRAIN_TEST_KAFKA")
	if brokers == "" {
		t.Skip("MOLDRAIN_TEST_KAFKA is not set")
	}
	c, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(brokers, ",")...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	run := time.Now().UnixNano()
	n := 0
	storetest.Run(t, func(t *testing.T) persist.Store {
		n++
		topic := fmt.Sprintf("moldrain-test-%d-%d", run, n)
		s, err := kafka.New(t.Context(), c, topic)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = kadm.NewClient(c).DeleteTopics(context.Background(), topic) })
		return s
	})
}
