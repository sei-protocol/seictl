BIN       := seictl
CMD       := ./cmd/seictl
BUILD_DIR := ./build

.PHONY: build install run test lint fmt clean

build:
	go build -o $(BUILD_DIR)/$(BIN) $(CMD)

install:
	go install $(CMD)

run:
	go run $(CMD) $(ARGS)

test:
	go test ./...

lint:
	gofmt -s -l .

fmt:
	gofmt -s -w .

clean:
	rm -rf $(BUILD_DIR)
