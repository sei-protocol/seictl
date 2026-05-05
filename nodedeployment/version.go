package nodedeployment

// version is stamped onto provenance annotations. Linker override:
//
//	-ldflags "-X 'github.com/sei-protocol/seictl/nodedeployment.version=$VERSION'"
//
// `make build` wires version.json; bare `go build`/`go test` see "dev".
var version = "dev"
