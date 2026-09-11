// Package mcp registers `seictl mcp`: a stdio Model Context Protocol server
// that exposes seictl's offline render verbs as typed tools. Every tool is
// pure — it runs the same validation and templates the CLI does and never
// opens a kubeconfig — so an agent gets JSON-schema'd inputs and a
// metav1.Status-shaped error instead of parsing --help and stderr.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sei-protocol/sei-k8s-controller/harness/bench"
	"github.com/sei-protocol/sei-k8s-controller/harness/faults"
	"github.com/urfave/cli/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seibench "github.com/sei-protocol/seictl/bench"
	"github.com/sei-protocol/seictl/chaos"
	"github.com/sei-protocol/seictl/internal/cliutil"
	"github.com/sei-protocol/seictl/seinetwork"
	"github.com/sei-protocol/seictl/seinode"
)

// Cmd is the `seictl mcp` command.
var Cmd = cli.Command{
	Name:  "mcp",
	Usage: "Serve seictl's offline render verbs as MCP tools over stdio",
	Description: "Speaks the Model Context Protocol on stdin/stdout for an agent host " +
		"(Claude Code, Cursor, ...). Tools: chaos_list, chaos_render, bench_render, " +
		"network_render, node_render. All are offline — no kubeconfig is read and " +
		"nothing is applied; the agent commits the returned manifests to its GitOps workspace.",
	Action: func(ctx context.Context, _ *cli.Command) error {
		if err := NewServer().Run(ctx, &mcp.StdioTransport{}); err != nil {
			cliutil.EmitStatus(os.Stderr, err)
			return cli.Exit("", 1)
		}
		return nil
	},
}

// ChaosListOutput is the fault catalog, same shape as `chaos list -o json`.
type ChaosListOutput struct {
	Faults []chaos.CatalogEntry `json:"faults"`
}

// ChaosRenderInput mirrors the flags of `seictl chaos render`.
type ChaosRenderInput struct {
	Fault     string `json:"fault" jsonschema:"catalog name from chaos_list, e.g. network-partition"`
	ChainID   string `json:"chainId" jsonschema:"SeiNetwork name; DNS-1123 label"`
	RunID     string `json:"runId" jsonschema:"run token; suffixes resource names and sets sei.io/harness-run; DNS-1123 label"`
	Namespace string `json:"namespace" jsonschema:"namespace of the SeiNetwork pods"`
	Duration  string `json:"duration,omitempty" jsonschema:"Go duration such as 2m or 90s; required unless the fault is oneShot, which rejects it"`
}

// BenchRenderInput mirrors the flags of `seictl bench render`.
type BenchRenderInput struct {
	RunID            string `json:"runId" jsonschema:"run token; names the Job seiload-<runId>; DNS-1123 label"`
	ChainID          string `json:"chainId" jsonschema:"chain under test; DNS-1123 label"`
	Image            string `json:"image" jsonschema:"seiload image reference, pinned by digest"`
	ProfileConfigMap string `json:"profileConfigMap" jsonschema:"ConfigMap holding profile.json"`
	DurationMinutes  int    `json:"durationMinutes" jsonschema:"load duration in whole minutes, > 0"`
	Namespace        string `json:"namespace,omitempty" jsonschema:"namespace for the Job"`
	Commit           string `json:"commit,omitempty" jsonschema:"sei-chain commit under test"`
	Workload         string `json:"workload,omitempty" jsonschema:"workload label; defaults to the harness default"`
	DeadlineSeconds  int    `json:"deadlineSeconds,omitempty" jsonschema:"activeDeadlineSeconds override; default durationMinutes + 15m"`
}

// NetworkRenderInput mirrors the flags of `seictl network apply`.
type NetworkRenderInput struct {
	Preset           string   `json:"preset" jsonschema:"preset name, e.g. genesis-chain"`
	Name             string   `json:"name" jsonschema:"SeiNetwork name"`
	Namespace        string   `json:"namespace,omitempty"`
	ChainID          string   `json:"chainId,omitempty" jsonschema:"spec.genesis.chainId"`
	Image            string   `json:"image,omitempty" jsonschema:"seid image reference"`
	Replicas         *int     `json:"replicas,omitempty" jsonschema:"validator count"`
	CPU              string   `json:"cpu,omitempty" jsonschema:"resource quantity, e.g. 4"`
	Memory           string   `json:"memory,omitempty" jsonschema:"resource quantity, e.g. 16Gi"`
	Storage          string   `json:"storage,omitempty" jsonschema:"data volume size, e.g. 500Gi"`
	IOPS             string   `json:"iops,omitempty"`
	Throughput       string   `json:"throughput,omitempty"`
	NodeIsolation    string   `json:"nodeIsolation,omitempty" jsonschema:"Shared or Dedicated"`
	ConsensusEngine  string   `json:"consensusEngine,omitempty" jsonschema:"Tendermint or Autobahn"`
	EvmOnly          bool     `json:"evmOnly,omitempty" jsonschema:"requires consensusEngine Autobahn"`
	Sets             []string `json:"set,omitempty" jsonschema:"field overrides in --set syntax (dotted path, equals sign, value)"`
	ConfigValues     []string `json:"configValue,omitempty" jsonschema:"spec.configValues entries in --config-value syntax"`
	GenesisAccounts  []string `json:"genesisAccount,omitempty" jsonschema:"address:balance pairs as --genesis-account takes them"`
	GenesisOverrides []string `json:"genesisOverride,omitempty" jsonschema:"dotted-path overrides for spec.genesis.overrides (same syntax as --genesis-override)"`
}

// NodeRenderInput mirrors the flags of `seictl node apply`.
type NodeRenderInput struct {
	Preset          string   `json:"preset" jsonschema:"preset name, e.g. rpc"`
	Name            string   `json:"name" jsonschema:"SeiNode name"`
	Namespace       string   `json:"namespace,omitempty"`
	ChainID         string   `json:"chainId,omitempty" jsonschema:"spec.chainId"`
	Image           string   `json:"image,omitempty" jsonschema:"seid image reference"`
	Network         string   `json:"network,omitempty" jsonschema:"SeiNetwork the node follows"`
	ExternalAddress string   `json:"externalAddress,omitempty"`
	CPU             string   `json:"cpu,omitempty"`
	Memory          string   `json:"memory,omitempty"`
	Storage         string   `json:"storage,omitempty"`
	IOPS            string   `json:"iops,omitempty"`
	Throughput      string   `json:"throughput,omitempty"`
	NodeIsolation   string   `json:"nodeIsolation,omitempty" jsonschema:"Shared or Dedicated"`
	ConsensusEngine string   `json:"consensusEngine,omitempty" jsonschema:"Tendermint or Autobahn"`
	EvmOnly         bool     `json:"evmOnly,omitempty"`
	Sets            []string `json:"set,omitempty" jsonschema:"field overrides in --set syntax (dotted path, equals sign, value)"`
	ConfigValues    []string `json:"configValue,omitempty" jsonschema:"spec.configValues entries in --config-value syntax"`
	Overrides       []string `json:"override,omitempty" jsonschema:"dotted-path overrides for spec.overrides (same syntax as --override)"`
}

// ManifestOutput carries one rendered Kubernetes manifest.
type ManifestOutput struct {
	Manifest string `json:"manifest" jsonschema:"YAML manifest ready to commit"`
}

// NewServer builds the MCP server with every tool registered.
func NewServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "seictl", Version: cliutil.Version}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name: "chaos_list",
		Description: "List the Chaos-Mesh fault catalog: name, kind, whether the fault is oneShot " +
			"(no duration) and whether it is meshWide (whole validator pool) or hits one validator.",
	}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, ChaosListOutput, error) {
		return nil, ChaosListOutput{Faults: chaos.Catalog()}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "chaos_render",
		Description: "Render one fault from the catalog as a Chaos-Mesh manifest targeting the " +
			"SeiNetwork's pods. Resources are named <fault>-<runId> and labelled sei.io/harness-run=<runId>.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in ChaosRenderInput) (*mcp.CallToolResult, ManifestOutput, error) {
		out, err := chaos.Render(in.Fault, faults.Params{
			ChainID: in.ChainID, RunID: in.RunID, Namespace: in.Namespace, Duration: in.Duration,
		})
		return manifestResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "bench_render",
		Description: "Render the seiload benchmark Job the nightly harness runs. The profile ConfigMap " +
			"itself is not rendered; it must hold profile.json.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in BenchRenderInput) (*mcp.CallToolResult, ManifestOutput, error) {
		workload := in.Workload
		if workload == "" {
			workload = bench.DefaultWorkload
		}
		out, err := seibench.Render(bench.Params{
			RunID:           in.RunID,
			ChainID:         in.ChainID,
			Commit:          in.Commit,
			Image:           in.Image,
			DurationMinutes: in.DurationMinutes,
			ProfileCM:       in.ProfileConfigMap,
			DeadlineSeconds: in.DeadlineSeconds,
			Namespace:       in.Namespace,
			Workload:        workload,
		})
		return manifestResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "network_render",
		Description: "Render the SeiNetwork manifest `seictl network apply` would send, with every " +
			"client-side validation applied (quantities, storage tiers, consensus, configValues). Nothing is applied.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in NetworkRenderInput) (*mcp.CallToolResult, ManifestOutput, error) {
		out, err := seinetwork.Manifest(seinetwork.ManifestArgs{
			Preset:           in.Preset,
			Name:             in.Name,
			Namespace:        in.Namespace,
			ChainID:          in.ChainID,
			Image:            in.Image,
			Replicas:         in.Replicas,
			CPU:              in.CPU,
			Memory:           in.Memory,
			Storage:          in.Storage,
			IOPS:             in.IOPS,
			Throughput:       in.Throughput,
			NodeIsolation:    in.NodeIsolation,
			ConsensusEngine:  in.ConsensusEngine,
			EvmOnly:          in.EvmOnly,
			Sets:             in.Sets,
			ConfigValues:     in.ConfigValues,
			GenesisAccounts:  in.GenesisAccounts,
			GenesisOverrides: in.GenesisOverrides,
		})
		return manifestResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "node_render",
		Description: "Render the SeiNode manifest `seictl node apply` would send, with every " +
			"client-side validation applied. Nothing is applied.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in NodeRenderInput) (*mcp.CallToolResult, ManifestOutput, error) {
		out, err := seinode.Manifest(seinode.ManifestArgs{
			Preset:          in.Preset,
			Name:            in.Name,
			Namespace:       in.Namespace,
			ChainID:         in.ChainID,
			Image:           in.Image,
			Network:         in.Network,
			ExternalAddress: in.ExternalAddress,
			CPU:             in.CPU,
			Memory:          in.Memory,
			Storage:         in.Storage,
			IOPS:            in.IOPS,
			Throughput:      in.Throughput,
			NodeIsolation:   in.NodeIsolation,
			ConsensusEngine: in.ConsensusEngine,
			EvmOnly:         in.EvmOnly,
			Sets:            in.Sets,
			ConfigValues:    in.ConfigValues,
			Overrides:       in.Overrides,
		})
		return manifestResult(out, err)
	})

	return s
}

// manifestResult turns a render error into an isError tool result whose
// text is the same metav1.Status JSON the CLI writes to stderr, so an agent
// discriminates on .reason exactly as it would with `jq -r .reason`.
func manifestResult(out []byte, err error) (*mcp.CallToolResult, ManifestOutput, error) {
	if err != nil {
		return nil, ManifestOutput{}, statusError{status: cliutil.ToStatus(asUsageError(err))}
	}
	return nil, ManifestOutput{Manifest: string(out)}, nil
}

// asUsageError keeps an apiserver-shaped error as is and classifies every
// other render failure as BadRequest, matching the CLI's stderr envelope.
func asUsageError(err error) error {
	var api apierrors.APIStatus
	if errors.As(err, &api) {
		return err
	}
	return cliutil.UsageError("%s", err.Error())
}

type statusError struct {
	status *metav1.Status
}

func (e statusError) Error() string {
	body, err := json.Marshal(e.status)
	if err != nil {
		return e.status.Message
	}
	return string(body)
}
