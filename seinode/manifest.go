package seinode

import (
	"sigs.k8s.io/yaml"
)

// ManifestArgs is the offline-render input for a SeiNode: the same fields
// `node apply` takes as flags. Namespace is written verbatim; nothing here
// touches a kubeconfig.
type ManifestArgs struct {
	Preset          string
	Name            string
	Namespace       string
	ChainID         string
	Image           string
	Network         string
	ExternalAddress string
	CPU             string
	Memory          string
	Storage         string
	IOPS            string
	Throughput      string
	NodeIsolation   string
	ConsensusEngine string
	EvmOnly         bool
	Sets            []string
	ConfigValues    []string
	Overrides       []string
}

// Manifest renders the SeiNode `node apply` would send, as YAML, with every
// client-side validation applied and no cluster access.
func Manifest(a ManifestArgs) ([]byte, error) {
	obj, err := render(renderArgs{
		preset:          a.Preset,
		name:            a.Name,
		namespace:       a.Namespace,
		chainID:         a.ChainID,
		image:           a.Image,
		network:         a.Network,
		externalAddress: a.ExternalAddress,
		cpu:             a.CPU,
		memory:          a.Memory,
		storage:         a.Storage,
		iops:            a.IOPS,
		throughput:      a.Throughput,
		nodeIsolation:   a.NodeIsolation,
		consensusEngine: a.ConsensusEngine,
		evmOnly:         a.EvmOnly,
		sets:            a.Sets,
		configValues:    a.ConfigValues,
		overrides:       a.Overrides,
	})
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(obj.Object)
}
