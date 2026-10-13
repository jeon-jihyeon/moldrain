<div align="center">
  <h1>moldrain</h1>
  <h3>Learns log templates from a stream the way Drain does, in Go.</h3>
</div>

<div align="center">
  <a href="https://github.com/jeon-jihyeon/moldrain/actions/workflows/test.yml"><img src="https://img.shields.io/github/actions/workflow/status/jeon-jihyeon/moldrain/test.yml?branch=main&label=test" alt="test"></a>
  <a href="https://goreportcard.com/report/github.com/jeon-jihyeon/moldrain"><img src="https://goreportcard.com/badge/github.com/jeon-jihyeon/moldrain" alt="go report"></a>
  <a href="https://pkg.go.dev/github.com/jeon-jihyeon/moldrain"><img src="https://pkg.go.dev/badge/github.com/jeon-jihyeon/moldrain.svg" alt="go reference"></a>
  <img src="https://img.shields.io/badge/license-MIT-green" alt="license">
</div>

<br>

Millions of log lines are a few hundred messages with different numbers in them. moldrain finds those messages as the lines arrive, `connected to <*> in <*>`, and gives each one a stable id, so you can count, alert on and aggregate messages instead of raw text.

It is a clean room implementation of Drain ([He et al., ICWS 2017](https://doi.org/10.1109/ICWS.2017.13)). The core module uses the standard library only.

## Quickstart

```
go get github.com/jeon-jihyeon/moldrain
```

```go
m, err := moldrain.New(moldrain.Options{MatchAfterTrain: true})
if err != nil {
	return err
}
for _, line := range []string{
	"connected to 10.0.0.1 in 12ms",
	"connected to 10.0.0.2 in 7ms",
	"disk full on sda",
	"disk full on sdb",
} {
	r := m.Train([]byte(line), time.Now())
	fmt.Printf("%-8s %s\n", r.Event, r.Template)
}
r, ok := m.Match([]byte("connected to 10.0.0.9 in 3ms"))
fmt.Println(ok, r.Template, len(r.Params))
```

```
created  connected to <*> in <*>
matched  connected to <*> in <*>
created  disk full on sda
changed  disk full on <*>
true connected to <*> in <*> 2
```

`Train` learns from a line and `Match` only reads. Any number of goroutines can call `Match` while one trains, since it reads a published view without taking the training lock.

## What it adds to Drain

| | |
|---|---|
| Template ids | a hash of the template text, so the same template gets the same id on every machine and after every restart |
| Change events | `created`, `matched`, `changed` when a template generalizes and `evicted`, plus aliases that map the id of a merged template to the one it joined, so whatever you keyed by an id can follow it |
| Tokenizers | plain, punctuation split, JSON and logfmt, with typed scanners for numbers, hex, IPs, UUIDs and times |
| Bounded memory | a cluster cap with eviction, and per template counts per time bucket |
| Persistence | a snapshot plus a change log. A file store is in the core, and Redis and Kafka stores are separate modules |
| Resolver | lines no template fits can be sent in batches to a model that picks a template or says new. `resolve/jev` is one for the Jev wire format |
| assay | window aggregates of templates: count, sum, min, max, mean, distinct and p50 to p99 of a variable, grouped by another variable |

## CLI

```
go install github.com/jeon-jihyeon/moldrain/cmd/moldrain@latest
```

```
$ moldrain mine app.log
3	5957c68988e8a4c3	connected to <*> in <*>
1	86f6405c2a80fc14	disk full on sda
```

`mine` learns and prints templates, `match` maps lines to templates it learned before, `show` prints a saved state, and `assay` aggregates. `--state <dir>` keeps what was learned between runs. Lines come from stdin when no file is given.

An assay profile names a template and what to compute from one of its variables:

```json
[{"name": "connect_latency", "match": "connected to <*> in <*>", "value": {"slot": 1}, "unit": "ms", "ops": ["count", "mean", "p95"]}]
```

```
$ moldrain assay --profiles profiles.json --window 1m app.log
{"start":"2026-10-13T13:39:00Z","end":"2026-10-13T13:40:00Z","profile":"connect_latency","op":"mean","value":13,"unit":"ms","lines":3,...}
```

## Modules

| Module | Adds |
|---|---|
| `github.com/jeon-jihyeon/moldrain` | the miner, tokenizers, `persist` with the file store, `resolve` and `assay`. Standard library only |
| `github.com/jeon-jihyeon/moldrain/store/redis` | a persist store on Redis |
| `github.com/jeon-jihyeon/moldrain/store/kafka` | a persist store on a compacted Kafka topic |
| `github.com/jeon-jihyeon/moldrain/resolve/jev` | a resolver backed by Jev |

## Benchmark

All 14 datasets of Loghub 2.0, 50.4 million lines, on an Apple M1 Pro. Accuracy is the average over the datasets, throughput is over all lines. Drain3 0.9.11 and [faceair/drain](https://github.com/faceair/drain) ran at their defaults.

| | GA | FGA | PA | FTA | Lines per second |
|---|---|---|---|---|---|
| moldrain | 0.820 | 0.595 | 0.451 | 0.315 | 1,426,722 |
| Drain3 | 0.761 | 0.601 | 0.412 | 0.271 | 121,248 |
| faceair/drain | 0.761 | 0.601 | 0.412 | 0.271 | 673,982 |

faceair/drain trains faster on 10 of the 14 datasets. moldrain is faster where many templates share a leaf, like HealthApp and OpenStack, and its time includes the per minute counts and the published view. The digit rule and the similarity formula were tuned on these same datasets, so the accuracy is not on held out data.

`bench` is its own module. `go run ./bench/fetch -dir <dir>` downloads the datasets from [Zenodo](https://zenodo.org/records/8275861) and checks their MD5 sums, and `go -C bench run . -data <dir>` runs the comparison. The datasets are not part of this repository.

## Credits

- Drain: Pinjia He, Jieming Zhu, Zibin Zheng and Michael R. Lyu. [Drain: An Online Log Parsing Approach with Fixed Depth Tree](https://doi.org/10.1109/ICWS.2017.13). ICWS 2017
- Loghub 2.0: Zhihan Jiang, Jinyang Liu, Junjie Huang, Yichen Li, Yintong Huo, Jiazhen Gu, Zhuangbin Chen, Jieming Zhu and Michael R. Lyu. [A Large-scale Evaluation for Log Parsing Techniques: How Far are We?](https://doi.org/10.1145/3650212.3652123). ISSTA 2024

## License

[MIT](LICENSE)
