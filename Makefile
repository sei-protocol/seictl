BIN       := seictl
BUILD_DIR := ./build

.PHONY: build install run test lint fmt clean

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

clean:
	rm -rf $(BUILD_DIR)
