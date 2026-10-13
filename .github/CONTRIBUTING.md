# Contributing to moldrain

Thanks for helping. Bug reports and fixes are welcome. For a security problem use the private reporting link in [SECURITY.md](SECURITY.md) instead of an issue.

## What you need

- Go at the version in [go.mod](../go.mod)
- [golangci-lint](https://golangci-lint.run) v2, the version CI pins in [test.yml](workflows/test.yml)

## Layout

The root module uses the standard library only. `store/redis`, `store/kafka`, `resolve/jev` and `bench` are Go modules of their own, joined by `go.work`, so a user pulls a client library only by importing its module. Until the root module has a tag, `go.work` resolves the root version they require to the checkout.

## Checks

Run these before opening a pull request, in the root and in every module you changed. CI runs them on Linux for every pull request, every push to `main` and every tag, and the tests on macOS too.

```sh
gofmt -l .
go vet ./...
golangci-lint run ./...
go test -race ./...
```

- `gofmt -l .` prints nothing when every file is formatted
- The Kafka store runs its contract suite against an in process broker in every `go test`
- The Redis and Kafka store tests against real servers carry the `integration` build tag and run only when `MOLDRAIN_TEST_REDIS` or `MOLDRAIN_TEST_KAFKA` holds a server address, such as `MOLDRAIN_TEST_REDIS=localhost:6379 go test -tags integration ./...` in `store/redis`

## Commits and pull requests

Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org) as `type(scope): subject`, such as `fix(tree): keep the salt of a colliding id after a rebuild`.

- `type` is one of `feat`, `fix`, `docs`, `test`, `refactor`, `chore` or `ci`
- Release notes group `feat` under Features and `fix` under Fixes, and leave merge commits out
- Keep one change per pull request and fill in the template
