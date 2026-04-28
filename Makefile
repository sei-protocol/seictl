BIN       := seictl
BUILD_DIR := ./build

.PHONY: build install run test test-envtest lint fmt generate clean

build:
	go build -o $(BUILD_DIR)/$(BIN) .

install:
	go install .

run:
	go run . $(ARGS)

test:
	go test ./...

ENVTEST_K8S_VERSION ?= 1.30.x

test-envtest:
	@KUBEBUILDER_ASSETS="$$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest use $(ENVTEST_K8S_VERSION) -p path)" \
		go test -tags=envtest ./cluster/internal/kube/...

lint:
	gofmt -s -l .

fmt:
	gofmt -s -w .

generate:
	go generate ./...

clean:
	rm -rf $(BUILD_DIR)
