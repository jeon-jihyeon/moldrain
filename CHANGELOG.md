# Changelog

All notable changes to moldrain are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed

- The file store fails a load with `ErrCorrupt` when a record before the last is damaged, instead of loading the records before it and compacting the rest away
- A change log without a snapshot fails the load with `ErrCorrupt` instead of being replaced by an empty snapshot

## [0.1.0] - 2026-10-21

### Added

- Optional versioned JSONL for `match` with parameters, alias identity, exact byte values and prefix truncation metadata
- Tokenizers for plain, punctuation split, JSON and logfmt lines with typed scanners for numbers, hex, IPs, UUIDs and times, allocation free after warm up
- Drain parse tree with branch positions picked by observed cardinality, template ids hashed from the template, and change events
- `Train` and a `Match` that takes no lock and reads a published view any number of goroutines share, a cluster cap with eviction, and per template counts per time bucket
- Persistence as a snapshot plus a change log, with a file store in the core and Redis and Kafka stores as separate modules
- Resolver for lines no template fits, with a Jev resolver as a separate module
- `assay`: window aggregates of profiles bound to templates, with count, sum, min, max, mean, distinct and percentiles, binding that follows Changed and Alias, and records delivered from a goroutine of their own
- `moldrain mine`, `match`, `show` and `assay`
- Benchmark against Drain3 and faceair/drain on Loghub 2.0
