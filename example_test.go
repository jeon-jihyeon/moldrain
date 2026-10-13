package moldrain_test

import (
	"fmt"
	"time"

	"github.com/jeon-jihyeon/moldrain"
)

func Example() {
	m, err := moldrain.New(moldrain.Options{MatchAfterTrain: true})
	if err != nil {
		panic(err)
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, line := range []string{
		"connected to 10.0.0.1 in 12ms",
		"connected to 10.0.0.2 in 7ms",
		"disk full on sda",
		"disk full on sdb",
	} {
		r := m.Train([]byte(line), at)
		fmt.Printf("%-8s %s\n", r.Event, r.Template)
	}
	r, ok := m.Match([]byte("connected to 10.0.0.9 in 3ms"))
	fmt.Println(ok, r.Template, len(r.Params))
	// Output:
	// created  connected to <*> in <*>
	// matched  connected to <*> in <*>
	// created  disk full on sda
	// changed  disk full on <*>
	// true connected to <*> in <*> 2
}

func ExampleMiner_Counts() {
	m, _ := moldrain.New(moldrain.Options{Bucket: time.Minute})
	at := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		m.Train([]byte(fmt.Sprintf("tick %d", i)), at.Add(time.Duration(i)*20*time.Second))
	}
	for _, c := range m.Counts(at, at.Add(time.Minute)) {
		fmt.Println(c.N)
	}
	// Output:
	// 3
}
