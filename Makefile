BIN     := bin
PKG     := ./...
GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: all build client operator ranger bearer mithlond eregion council \
        test race vet fmt lint cover clean

all: build

# Everything, because docs/deploying.md tells operators to run mithlond,
# eregion and council, and a Makefile that quietly builds only two of the five
# sends them looking for binaries that were never produced.
build: client operator

# What a user runs.
client: bearer

# What an operator runs. A relay is on both lists: relay operators are the one
# group that is neither running the network nor merely using it.
operator: ranger mithlond eregion council

ranger:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)/ranger ./cmd/ranger

bearer:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)/bearer ./cmd/bearer

mithlond:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)/mithlond ./cmd/mithlond

eregion:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)/eregion ./cmd/eregion

council:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)/council ./cmd/council

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
