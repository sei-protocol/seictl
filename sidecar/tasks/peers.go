package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seictl/sidecar/rpc"
	"github.com/sei-protocol/seilog"
)

var peerLog = seilog.NewLogger("seictl", "task", "peers")

const p2pPort = "26656"

// PeerSource discovers peers from a specific source type.
type PeerSource interface {
	Discover(ctx context.Context) ([]string, error)
}

// EC2DescribeAPI abstracts the EC2 DescribeInstances call for testing.
type EC2DescribeAPI interface {
	DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

// EC2ClientFactory builds an EC2 client for a given region.
type EC2ClientFactory func(ctx context.Context, region string) (EC2DescribeAPI, error)

// DefaultEC2ClientFactory creates a real EC2 client using Pod Identity credentials.
func DefaultEC2ClientFactory(ctx context.Context, region string) (EC2DescribeAPI, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return ec2.NewFromConfig(cfg), nil
}

// NodeIDQuerier fetches a Tendermint node ID from an IP address.
type NodeIDQuerier func(ctx context.Context, ip string) (string, error)

// EC2TagsSource discovers peers by querying EC2 for running instances
// matching the configured tags.
type EC2TagsSource struct {
	Region      string
	Tags        map[string]string
	EC2Factory  EC2ClientFactory
	QueryNodeID NodeIDQuerier
}

// StaticSource returns a fixed list of peer addresses.
type StaticSource struct {
	Addresses []string
}

// DNSEndpointsSource discovers peers by querying a list of DNS endpoints
// for their Tendermint node IDs. The controller resolves Kubernetes
// SeiNodes to stable pod DNS names; this source handles the RPC query.
type DNSEndpointsSource struct {
	Endpoints   []string
	QueryNodeID NodeIDQuerier
}

// DiscoverPeersRequest holds the typed parameters for the discover-peers task.
type DiscoverPeersRequest struct {
	Sources []PeerSourceEntry `json:"sources"`
}

// PeerSourceEntry represents a single peer source in the params JSON.
type PeerSourceEntry struct {
	Type      string            `json:"type"`
	Region    string            `json:"region,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	Addresses []string          `json:"addresses,omitempty"`
	Endpoints []string          `json:"endpoints,omitempty"`
}

// PeerDiscoverer resolves peers from multiple sources and writes them to a file.
type PeerDiscoverer struct {
	homeDir          string
	ec2ClientFactory EC2ClientFactory
	queryNodeID      NodeIDQuerier
}

// Discover queries EC2 for running instances matching the configured tags
// and returns peer addresses in id@host:port format.
func (s *EC2TagsSource) Discover(ctx context.Context) ([]string, error) {
	factory := s.EC2Factory
	if factory == nil {
		factory = DefaultEC2ClientFactory
	}
	querier := s.QueryNodeID
	if querier == nil {
		querier = defaultQueryNodeID
	}

	ec2Client, err := factory(ctx, s.Region)
	if err != nil {
		return nil, fmt.Errorf("building EC2 client for region %s: %w", s.Region, err)
	}

	filters := []ec2types.Filter{
		{
			Name:   aws.String("instance-state-name"),
			Values: []string{"running"},
		},
	}
	for k, v := range s.Tags {
		filters = append(filters, ec2types.Filter{
			Name:   aws.String("tag:" + k),
			Values: []string{v},
		})
	}

	output, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: filters,
	})
	if err != nil {
		return nil, fmt.Errorf("ec2 DescribeInstances: %w", err)
	}

	var peers []string
	for _, reservation := range output.Reservations {
		for _, instance := range reservation.Instances {
			peer, err := buildPeerAddress(ctx, querier, instance)
			if err != nil {
				continue
			}
			peers = append(peers, peer)
		}
	}

	if len(peers) == 0 {
		return nil, fmt.Errorf("no reachable peers found via ec2Tags in region %s", s.Region)
	}

	return peers, nil
}

// Discover returns the statically configured peer addresses.
func (s *StaticSource) Discover(_ context.Context) ([]string, error) {
	if len(s.Addresses) == 0 {
		return nil, fmt.Errorf("static peer source has no addresses")
	}
	result := make([]string, len(s.Addresses))
	copy(result, s.Addresses)
	return result, nil
}

// Discover queries each DNS endpoint's Tendermint RPC for its node ID
// and returns peer addresses in id@host:port format.
func (s *DNSEndpointsSource) Discover(ctx context.Context) ([]string, error) {
	querier := s.QueryNodeID
	if querier == nil {
		querier = defaultQueryNodeID
	}

	var peers []string
	for _, endpoint := range s.Endpoints {
		nodeID, err := querier(ctx, endpoint)
		if err != nil {
			peerLog.Info("skipping unreachable DNS endpoint", "endpoint", endpoint, "error", err)
			continue
		}
		peers = append(peers, fmt.Sprintf("%s@%s:%s", nodeID, endpoint, p2pPort))
	}

	if len(peers) == 0 {
		return nil, fmt.Errorf("no reachable peers found via dnsEndpoints (%d endpoints tried)", len(s.Endpoints))
	}
	return peers, nil
}

// NewPeerDiscoverer creates a discoverer targeting the given home directory.
func NewPeerDiscoverer(homeDir string, ec2Factory EC2ClientFactory, nodeIDQuerier NodeIDQuerier) *PeerDiscoverer {
	if ec2Factory == nil {
		ec2Factory = DefaultEC2ClientFactory
	}
	if nodeIDQuerier == nil {
		nodeIDQuerier = defaultQueryNodeID
	}
	return &PeerDiscoverer{
		homeDir:          homeDir,
		ec2ClientFactory: ec2Factory,
		queryNodeID:      nodeIDQuerier,
	}
}

// Handler returns an engine.TaskHandler that parses peer source params and
// dispatches to the appropriate PeerSource implementations.
//
// Expected params format:
//
//	{"sources": [{"type": "ec2Tags", "region": "...", "tags": {...}}, ...]}
func (d *PeerDiscoverer) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, params DiscoverPeersRequest) error {
		if len(params.Sources) == 0 {
			return fmt.Errorf("discover-peers: missing required param 'sources'")
		}

		sources, err := d.buildSources(params.Sources)
		if err != nil {
			return fmt.Errorf("discover-peers: %w", err)
		}

		peers, err := discoverFromSources(ctx, sources)
		if err != nil {
			return err
		}

		return writePeersToConfig(d.homeDir, peers)
	})
}

// discoverFromSources iterates all sources, collects peers, and deduplicates.
func discoverFromSources(ctx context.Context, sources []PeerSource) ([]string, error) {
	seen := make(map[string]bool)
	var all []string

	for _, src := range sources {
		peers, err := src.Discover(ctx)
		if err != nil {
			return nil, err
		}
		peerLog.Debug("source returned peers", "count", len(peers))
		for _, p := range peers {
			if !seen[p] {
				seen[p] = true
				all = append(all, p)
			}
		}
	}

	if len(all) == 0 {
		return nil, fmt.Errorf("no peers discovered from any source")
	}

	peerLog.Info("peers discovered", "total", len(all))
	return all, nil
}

// buildSources converts typed source configs into PeerSource instances.
func (d *PeerDiscoverer) buildSources(configs []PeerSourceEntry) ([]PeerSource, error) {
	var sources []PeerSource
	for i, cfg := range configs {
		switch cfg.Type {
		case "ec2Tags":
			if cfg.Region == "" {
				return nil, fmt.Errorf("source[%d]: ec2Tags source missing required field 'region'", i)
			}
			if len(cfg.Tags) == 0 {
				return nil, fmt.Errorf("source[%d]: ec2Tags source has empty tags", i)
			}
			sources = append(sources, &EC2TagsSource{
				Region:      cfg.Region,
				Tags:        cfg.Tags,
				EC2Factory:  d.ec2ClientFactory,
				QueryNodeID: d.queryNodeID,
			})
		case "static":
			if len(cfg.Addresses) == 0 {
				return nil, fmt.Errorf("source[%d]: static source has empty addresses", i)
			}
			sources = append(sources, &StaticSource{Addresses: cfg.Addresses})
		case "dnsEndpoints":
			if len(cfg.Endpoints) == 0 {
				return nil, fmt.Errorf("source[%d]: dnsEndpoints source has empty endpoints", i)
			}
			sources = append(sources, &DNSEndpointsSource{
				Endpoints:   cfg.Endpoints,
				QueryNodeID: d.queryNodeID,
			})
		default:
			return nil, fmt.Errorf("source[%d]: unknown type %q", i, cfg.Type)
		}
	}
	return sources, nil
}

func buildPeerAddress(ctx context.Context, querier NodeIDQuerier, instance ec2types.Instance) (string, error) {
	ip := instanceIP(instance)
	if ip == "" {
		return "", fmt.Errorf("instance has no IP address")
	}

	nodeID, err := querier(ctx, ip)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s@%s:%s", nodeID, ip, p2pPort), nil
}

// instanceIP returns the public IP if available, falling back to private.
// Public IPs are needed because peers may span VPCs or run outside AWS.
func instanceIP(instance ec2types.Instance) string {
	if instance.PublicIpAddress != nil && *instance.PublicIpAddress != "" {
		return *instance.PublicIpAddress
	}
	if instance.PrivateIpAddress != nil {
		return *instance.PrivateIpAddress
	}
	return ""
}

func defaultQueryNodeID(ctx context.Context, ip string) (string, error) {
	c := rpc.NewClient(fmt.Sprintf("http://%s:26657", ip), nil)
	raw, err := c.Get(ctx, "/status")
	if err != nil {
		return "", fmt.Errorf("GET /status: %w", err)
	}

	var status rpc.StatusResult
	if err := json.Unmarshal(raw, &status); err != nil {
		return "", fmt.Errorf("parsing /status response: %w", err)
	}

	if status.NodeInfo.ID == "" {
		return "", fmt.Errorf("empty node ID in /status response from %s", ip)
	}

	return status.NodeInfo.ID, nil
}

func writePeersToConfig(homeDir string, peers []string) error {
	configPath := filepath.Join(homeDir, "config", "config.toml")
	peersPatch := map[string]any{
		"p2p": map[string]any{
			"persistent-peers": strings.Join(peers, ","),
		},
	}
	return mergeAndWrite(configPath, peersPatch)
}
