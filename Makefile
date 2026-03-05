BIN       := seictl
BUILD_DIR := ./build

.PHONY: build install run test lint fmt generate clean

build:
	go build -o $(BUILD_DIR)/$(BIN) .

install:
	go install .

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
