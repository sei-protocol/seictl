package seinetwork

import (
	"sigs.k8s.io/yaml"

	"github.com/sei-protocol/seictl/internal/cliutil"
)

// ManifestArgs is the offline-render input for a SeiNetwork: the same fields
// `network apply` takes as flags. Namespace is written verbatim; nothing here
// touches a kubeconfig, so it is required: `apply` fills it from the
// kubeconfig, and render()'s anti-retarget reassertion depends on it.
type ManifestArgs struct {
	Preset           string
	Name             string
	Namespace        string
	ChainID          string
	Image            string
	Replicas         *int
	CPU              string
	Memory           string
	Storage          string
	IOPS             string
	Throughput       string
	NodeIsolation    string
	ConsensusEngine  string
	EvmOnly          bool
	Sets             []string
	ConfigValues     []string
	GenesisAccounts  []string
	GenesisOverrides []string
}

// Manifest renders the SeiNetwork `network apply` would send, as YAML, with
// every client-side validation applied and no cluster access.
func Manifest(a ManifestArgs) ([]byte, error) {
	if a.Namespace == "" {
		return nil, cliutil.UsageError("namespace is required: apply takes it from the kubeconfig; the offline render has none")
	}
	args := renderArgs{
		preset:           a.Preset,
		name:             a.Name,
		namespace:        a.Namespace,
		chainID:          a.ChainID,
		image:            a.Image,
		cpu:              a.CPU,
		memory:           a.Memory,
		storage:          a.Storage,
		iops:             a.IOPS,
		throughput:       a.Throughput,
		nodeIsolation:    a.NodeIsolation,
		consensusEngine:  a.ConsensusEngine,
		evmOnly:          a.EvmOnly,
		sets:             a.Sets,
		configValues:     a.ConfigValues,
		genesisAccounts:  a.GenesisAccounts,
		genesisOverrides: a.GenesisOverrides,
	}
	if a.Replicas != nil {
		args.replicas = *a.Replicas
		args.hasReps = true
	}
	obj, err := render(args)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(obj.Object)
}
