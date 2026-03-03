FROM docker.io/golang:1.25 as BUILD

WORKDIR /go/src/seictl

COPY go.mod go.sum ./
COPY sei-sidecar/go.mod sei-sidecar/go.sum ./sei-sidecar/
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /go/bin/seictl .

FROM gcr.io/distroless/static-debian12
COPY --from=BUILD /go/bin/seictl /usr/bin/

ENTRYPOINT ["/usr/bin/seictl"]