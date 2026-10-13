package moldrain_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeon-jihyeon/moldrain"
)

func BenchmarkTrain(b *testing.B) {
	lines := genLines(20000)
	bl := make([][]byte, len(lines))
	for i, l := range lines {
		bl[i] = []byte(l)
	}
	m := newMiner(b, moldrain.Options{})
	for _, l := range bl {
		m.Train(l, t0)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Train(bl[i%len(bl)], t0)
	}
}

// Match at 1, 4 and 8 goroutines while one goroutine trains
func BenchmarkMatchScaling(b *testing.B) {
	lines := genLines(20000)
	bl := make([][]byte, len(lines))
	for i, l := range lines {
		bl[i] = []byte(l)
	}
	for _, g := range []int{1, 4, 8} {
		b.Run(fmt.Sprintf("goroutines=%d", g), func(b *testing.B) {
			m := newMiner(b, moldrain.Options{})
			for _, l := range bl {
				m.Train(l, t0)
			}
			m.Publish()
			stop := make(chan struct{})
			var trained atomic.Int64
			go func() {
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					m.Train(bl[i%len(bl)], t0)
					trained.Add(1)
				}
			}()
			b.ReportAllocs()
			b.ResetTimer()
			start := time.Now()
			var wg sync.WaitGroup
			per := b.N/g + 1
			for w := 0; w < g; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					for i := 0; i < per; i++ {
						m.Match(bl[(w*7919+i)%len(bl)])
					}
				}(w)
			}
			wg.Wait()
			elapsed := time.Since(start)
			b.StopTimer()
			close(stop)
			b.ReportMetric(float64(per*g)/elapsed.Seconds(), "matches/s")
			b.ReportMetric(float64(trained.Load())/elapsed.Seconds(), "trains/s")
		})
	}
}
