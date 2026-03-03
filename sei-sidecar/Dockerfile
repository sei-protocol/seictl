FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o sei-sidecar cmd/main.go

FROM gcr.io/distroless/static
WORKDIR /
COPY --from=builder /workspace/sei-sidecar .

ENTRYPOINT ["/sei-sidecar"]
