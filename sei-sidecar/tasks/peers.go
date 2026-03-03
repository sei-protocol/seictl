package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/sei-protocol/seictl/sei-sidecar/engine"
)

const peersFile = ".sei-sidecar-peers.json"

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
	Region       string
	Tags         map[string]string
	EC2Factory   EC2ClientFactory
	QueryNodeID  NodeIDQuerier
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

// StaticSource returns a fixed list of peer addresses.
type StaticSource struct {
	Addresses []string
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

// PeerDiscoverer resolves peers from multiple sources and writes them to a file.
type PeerDiscoverer struct {
	homeDir          string
	ec2ClientFactory EC2ClientFactory
	queryNodeID      NodeIDQuerier
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
// Supports two param formats:
//   - New: {"sources": [{"type": "ec2Tags", "region": "...", "tags": {...}}, ...]}
//   - Legacy: {"region": "...", "chainId": "..."} (backward compat)
func (d *PeerDiscoverer) Handler() engine.TaskHandler {
	return func(ctx context.Context, params map[string]any) error {
		var sources []PeerSource

		if rawSources, ok := params["sources"]; ok {
			parsed, err := d.parseSources(rawSources)
			if err != nil {
				return fmt.Errorf("discover-peers: %w", err)
			}
			sources = parsed
		} else {
			legacy, err := d.parseLegacyParams(params)
			if err != nil {
				return err
			}
			sources = []PeerSource{legacy}
		}

		peers, err := discoverFromSources(ctx, sources)
		if err != nil {
			return err
		}

		return writePeersFile(d.homeDir, peers)
	}
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

	return all, nil
}

// parseSources converts the raw "sources" param into typed PeerSource instances.
func (d *PeerDiscoverer) parseSources(raw any) ([]PeerSource, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("sources must be a list")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("sources list is empty")
	}

	var sources []PeerSource
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("source[%d] is not an object", i)
		}

		typ, _ := m["type"].(string)
		switch typ {
		case "ec2Tags":
			src, err := d.parseEC2TagsSource(m)
			if err != nil {
				return nil, fmt.Errorf("source[%d]: %w", i, err)
			}
			sources = append(sources, src)
		case "static":
			src, err := parseStaticSource(m)
			if err != nil {
				return nil, fmt.Errorf("source[%d]: %w", i, err)
			}
			sources = append(sources, src)
		default:
			return nil, fmt.Errorf("source[%d]: unknown type %q", i, typ)
		}
	}

	return sources, nil
}

func (d *PeerDiscoverer) parseEC2TagsSource(m map[string]any) (*EC2TagsSource, error) {
	region, _ := m["region"].(string)
	if region == "" {
		return nil, fmt.Errorf("ec2Tags source missing required field 'region'")
	}

	rawTags, ok := m["tags"]
	if !ok {
		return nil, fmt.Errorf("ec2Tags source missing required field 'tags'")
	}

	tags, err := toStringMap(rawTags)
	if err != nil {
		return nil, fmt.Errorf("ec2Tags tags: %w", err)
	}
	if len(tags) == 0 {
		return nil, fmt.Errorf("ec2Tags source has empty tags")
	}

	return &EC2TagsSource{
		Region:      region,
		Tags:        tags,
		EC2Factory:  d.ec2ClientFactory,
		QueryNodeID: d.queryNodeID,
	}, nil
}

func parseStaticSource(m map[string]any) (*StaticSource, error) {
	rawAddrs, ok := m["addresses"]
	if !ok {
		return nil, fmt.Errorf("static source missing required field 'addresses'")
	}

	addrs, err := toStringSlice(rawAddrs)
	if err != nil {
		return nil, fmt.Errorf("static addresses: %w", err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("static source has empty addresses")
	}

	return &StaticSource{Addresses: addrs}, nil
}

// parseLegacyParams handles the old {region, chainId} param format for backward compat.
func (d *PeerDiscoverer) parseLegacyParams(params map[string]any) (*EC2TagsSource, error) {
	region, _ := params["region"].(string)
	chainID, _ := params["chainId"].(string)

	if region == "" {
		return nil, fmt.Errorf("discover-peers: missing required param 'region'")
	}
	if chainID == "" {
		return nil, fmt.Errorf("discover-peers: missing required param 'chainId'")
	}

	return &EC2TagsSource{
		Region: region,
		Tags: map[string]string{
			"ChainIdentifier": chainID,
			"Component":       "snapshotter",
		},
		EC2Factory:  d.ec2ClientFactory,
		QueryNodeID: d.queryNodeID,
	}, nil
}

// Discover queries EC2 for running instances tagged with the given chain ID.
// Deprecated: Use EC2TagsSource directly or the sources-based Handler.
func (d *PeerDiscoverer) Discover(ctx context.Context, region, chainID string) ([]string, error) {
	src := &EC2TagsSource{
		Region: region,
		Tags: map[string]string{
			"ChainIdentifier": chainID,
			"Component":       "snapshotter",
		},
		EC2Factory:  d.ec2ClientFactory,
		QueryNodeID: d.queryNodeID,
	}
	return src.Discover(ctx)
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

	return fmt.Sprintf("%s@%s:26656", nodeID, ip), nil
}

func instanceIP(instance ec2types.Instance) string {
	if instance.PublicIpAddress != nil && *instance.PublicIpAddress != "" {
		return *instance.PublicIpAddress
	}
	if instance.PrivateIpAddress != nil {
		return *instance.PrivateIpAddress
	}
	return ""
}

// tendermintStatusResponse is the minimal shape of the /status JSON response.
type tendermintStatusResponse struct {
	NodeInfo struct {
		ID string `json:"id"`
	} `json:"node_info"`
	SyncInfo struct {
		LatestBlockHeight string `json:"latest_block_height"`
	} `json:"sync_info"`
}

func defaultQueryNodeID(ctx context.Context, ip string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, fmt.Sprintf("http://%s:26657/status", ip), nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET /status: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response body: %w", err)
	}

	var status tendermintStatusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		return "", fmt.Errorf("parsing /status response: %w", err)
	}

	if status.NodeInfo.ID == "" {
		return "", fmt.Errorf("empty node ID in /status response from %s", ip)
	}

	return status.NodeInfo.ID, nil
}

func writePeersFile(homeDir string, peers []string) error {
	data, err := json.Marshal(peers)
	if err != nil {
		return fmt.Errorf("marshaling peers: %w", err)
	}
	path := filepath.Join(homeDir, peersFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing peers file: %w", err)
	}
	return nil
}

// ReadPeersFile reads the peer list written by discover-peers.
func ReadPeersFile(homeDir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(homeDir, peersFile))
	if err != nil {
		return nil, err
	}
	var peers []string
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil, fmt.Errorf("parsing peers file: %w", err)
	}
	return peers, nil
}

// toStringMap converts an any to map[string]string.
func toStringMap(raw any) (map[string]string, error) {
	switch v := raw.(type) {
	case map[string]string:
		return v, nil
	case map[string]any:
		result := make(map[string]string, len(v))
		for k, val := range v {
			s, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("value for key %q is not a string", k)
			}
			result[k] = s
		}
		return result, nil
	default:
		return nil, fmt.Errorf("expected a map, got %T", raw)
	}
}

// toStringSlice converts an any to []string.
func toStringSlice(raw any) ([]string, error) {
	switch v := raw.(type) {
	case []string:
		return v, nil
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("list item is not a string")
			}
			result = append(result, s)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("expected a list, got %T", raw)
	}
}
