BIN       := seictl
BUILD_DIR := ./build
VERSION   := $(shell jq -r .version version.json)

LDFLAGS := -X 'github.com/sei-protocol/seictl/nodedeployment.version=$(VERSION)'

.PHONY: build install run test lint fmt generate clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BIN) .

install:
	go install -ldflags "$(LDFLAGS)" .

run:
	go run . $(ARGS)

test:
	go test ./...

lint:
	gofmt -s -l .

fmt:
	gofmt -s -w .

generate:
	go generate ./...

clean:
	rm -rf $(BUILD_DIR)
