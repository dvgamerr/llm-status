.PHONY: fmt tidy staticcheck vuln test coverage vet check build build-pi package clean

fmt:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -type f))"

tidy:
	go mod verify
	go mod tidy -diff

staticcheck:
	go tool staticcheck ./...

vuln:
	go tool govulncheck ./...

test:
	go test ./...

coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	@coverage=$$(go tool cover -func=coverage.out | awk '/^total:/ {gsub(/%/, "", $$3); print $$3}'); \
	awk -v coverage="$$coverage" 'BEGIN { if (coverage < 80) exit 1 }'

vet:
	go vet ./...

check: fmt tidy staticcheck vuln test coverage vet build-pi

build:
	go build -trimpath -o bin/llm-status ./cmd/llm-status

build-pi:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/llm-status-linux-arm64 ./cmd/llm-status

package:
	bash scripts/package.sh $${VERSION:?set VERSION, for example VERSION=v0.1.0}

clean:
	go clean
