BIN     := bin
PKG     := ./...
GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: all build ranger bearer test race vet fmt lint cover clean

all: build

build: ranger bearer

ranger:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)/ranger ./cmd/ranger

bearer:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)/bearer ./cmd/bearer

test:
	go test $(PKG)

# The relay is a concurrent byte pump; the race detector is not optional here.
race:
	go test -race $(PKG)

vet:
	go vet $(PKG)

fmt:
	gofmt -l -w .

lint: vet
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt found unformatted files" && exit 1)

cover:
	go test -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -1

clean:
	rm -rf $(BIN) coverage.out
