package nodedeployment

// version is the seictl release stamped onto provenance annotations on
// applied SNDs. Overridden at link time via:
//
//	go build -ldflags "-X 'github.com/sei-protocol/seictl/nodedeployment.version=$VERSION'"
//
// `make build` reads version.json and passes it through; bare `go
// build` and `go test` see "dev".
var version = "dev"
