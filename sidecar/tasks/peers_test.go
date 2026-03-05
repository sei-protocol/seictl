package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type mockEC2Client struct {
	output    *ec2.DescribeInstancesOutput
	err       error
	lastInput *ec2.DescribeInstancesInput
}

func (m *mockEC2Client) DescribeInstances(_ context.Context, input *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	m.lastInput = input
	return m.output, m.err
}

func mockEC2Factory(client EC2DescribeAPI) EC2ClientFactory {
	return func(_ context.Context, _ string) (EC2DescribeAPI, error) {
		return client, nil
	}
}

func staticNodeIDQuerier(id string) NodeIDQuerier {
	return func(_ context.Context, _ string) (string, error) {
		return id, nil
	}
}

func ensureConfigDir(t *testing.T, homeDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(homeDir, "config"), 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
}

func readPeersFromConfigTOML(t *testing.T, homeDir string) string {
	t.Helper()
	doc := readTOML(t, filepath.Join(homeDir, "config", "config.toml"))
	p2p, ok := doc["p2p"].(map[string]any)
	if !ok {
		t.Fatal("missing p2p section in config.toml")
	}
	peers, _ := p2p["persistent-peers"].(string)
	return peers
}

func TestEC2TagsSource_CustomTags(t *testing.T) {
	mock := &mockEC2Client{
		output: &ec2.DescribeInstancesOutput{
			Reservations: []ec2types.Reservation{
				{Instances: []ec2types.Instance{
					{PrivateIpAddress: aws.String("10.0.1.1")},
				}},
			},
		},
	}

	src := &EC2TagsSource{
		Region: "eu-central-1",
		Tags: map[string]string{
			"Environment": "production",
			"Role":        "validator",
		},
		EC2Factory:  mockEC2Factory(mock),
		QueryNodeID: staticNodeIDQuerier("node1"),
	}

	peers, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if len(peers) != 1 || peers[0] != "node1@10.0.1.1:26656" {
		t.Fatalf("unexpected peers: %v", peers)
	}

	if mock.lastInput == nil {
		t.Fatal("expected DescribeInstances to be called")
	}
	filterMap := make(map[string]string)
	for _, f := range mock.lastInput.Filters {
		if f.Name != nil && len(f.Values) > 0 {
			filterMap[*f.Name] = f.Values[0]
		}
	}
	if filterMap["tag:Environment"] != "production" {
		t.Errorf("missing or wrong tag:Environment filter: %v", filterMap)
	}
	if filterMap["tag:Role"] != "validator" {
		t.Errorf("missing or wrong tag:Role filter: %v", filterMap)
	}
	if filterMap["instance-state-name"] != "running" {
		t.Errorf("missing instance-state-name filter: %v", filterMap)
	}
}

func TestEC2TagsSource_NoPeers_ReturnsError(t *testing.T) {
	mock := &mockEC2Client{
		output: &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{}},
	}

	src := &EC2TagsSource{
		Region:      "us-east-1",
		Tags:        map[string]string{"ChainIdentifier": "test"},
		EC2Factory:  mockEC2Factory(mock),
		QueryNodeID: staticNodeIDQuerier("abc"),
	}

	_, err := src.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error when no peers found")
	}
}

func TestEC2TagsSource_SkipsUnreachableInstances(t *testing.T) {
	querier := func(_ context.Context, ip string) (string, error) {
		if ip == "10.0.1.1" {
			return "", fmt.Errorf("connection refused")
		}
		return "node2", nil
	}

	mock := &mockEC2Client{
		output: &ec2.DescribeInstancesOutput{
			Reservations: []ec2types.Reservation{
				{Instances: []ec2types.Instance{
					{PrivateIpAddress: aws.String("10.0.1.1")},
					{PrivateIpAddress: aws.String("10.0.1.2")},
				}},
			},
		},
	}

	src := &EC2TagsSource{
		Region:      "us-east-1",
		Tags:        map[string]string{"Env": "test"},
		EC2Factory:  mockEC2Factory(mock),
		QueryNodeID: querier,
	}

	peers, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if len(peers) != 1 || peers[0] != "node2@10.0.1.2:26656" {
		t.Fatalf("expected 1 reachable peer, got %v", peers)
	}
}

func TestEC2TagsSource_EC2Error(t *testing.T) {
	mock := &mockEC2Client{err: fmt.Errorf("access denied")}
	src := &EC2TagsSource{
		Region:      "us-east-1",
		Tags:        map[string]string{"Env": "test"},
		EC2Factory:  mockEC2Factory(mock),
		QueryNodeID: staticNodeIDQuerier("abc"),
	}

	_, err := src.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error on EC2 failure")
	}
}

func TestStaticSource_ReturnsPeers(t *testing.T) {
	src := &StaticSource{
		Addresses: []string{"abc@1.2.3.4:26656", "def@5.6.7.8:26656"},
	}

	peers, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}
	if peers[0] != "abc@1.2.3.4:26656" {
		t.Errorf("peer[0] = %q, want abc@1.2.3.4:26656", peers[0])
	}
}

func TestStaticSource_EmptyAddresses_ReturnsError(t *testing.T) {
	src := &StaticSource{Addresses: []string{}}
	_, err := src.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error for empty addresses")
	}
}

func TestDiscoverFromSources_Deduplication(t *testing.T) {
	src1 := &StaticSource{Addresses: []string{"abc@1.2.3.4:26656", "def@5.6.7.8:26656"}}
	src2 := &StaticSource{Addresses: []string{"def@5.6.7.8:26656", "ghi@9.10.11.12:26656"}}

	peers, err := discoverFromSources(context.Background(), []PeerSource{src1, src2})
	if err != nil {
		t.Fatalf("discoverFromSources failed: %v", err)
	}
	if len(peers) != 3 {
		t.Fatalf("expected 3 unique peers, got %d: %v", len(peers), peers)
	}

	expected := []string{"abc@1.2.3.4:26656", "def@5.6.7.8:26656", "ghi@9.10.11.12:26656"}
	for i, want := range expected {
		if peers[i] != want {
			t.Errorf("peer[%d] = %q, want %q", i, peers[i], want)
		}
	}
}

func TestDiscoverFromSources_MixedTypes(t *testing.T) {
	mock := &mockEC2Client{
		output: &ec2.DescribeInstancesOutput{
			Reservations: []ec2types.Reservation{
				{Instances: []ec2types.Instance{
					{PrivateIpAddress: aws.String("10.0.1.1")},
				}},
			},
		},
	}

	ec2Src := &EC2TagsSource{
		Region:      "us-east-1",
		Tags:        map[string]string{"Env": "test"},
		EC2Factory:  mockEC2Factory(mock),
		QueryNodeID: staticNodeIDQuerier("ec2node"),
	}
	staticSrc := &StaticSource{
		Addresses: []string{"static@1.2.3.4:26656"},
	}

	peers, err := discoverFromSources(context.Background(), []PeerSource{ec2Src, staticSrc})
	if err != nil {
		t.Fatalf("discoverFromSources failed: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d: %v", len(peers), peers)
	}
	if peers[0] != "ec2node@10.0.1.1:26656" {
		t.Errorf("peer[0] = %q, want ec2node@10.0.1.1:26656", peers[0])
	}
	if peers[1] != "static@1.2.3.4:26656" {
		t.Errorf("peer[1] = %q, want static@1.2.3.4:26656", peers[1])
	}
}

func TestHandler_SourcesParam_EC2Tags(t *testing.T) {
	homeDir := t.TempDir()
	ensureConfigDir(t, homeDir)
	mock := &mockEC2Client{
		output: &ec2.DescribeInstancesOutput{
			Reservations: []ec2types.Reservation{
				{Instances: []ec2types.Instance{
					{PrivateIpAddress: aws.String("10.0.1.1")},
				}},
			},
		},
	}

	discoverer := NewPeerDiscoverer(homeDir, mockEC2Factory(mock), staticNodeIDQuerier("node1"))
	handler := discoverer.Handler()

	err := handler(context.Background(), map[string]any{
		"sources": []any{
			map[string]any{
				"type":   "ec2Tags",
				"region": "eu-central-1",
				"tags": map[string]any{
					"ChainIdentifier": "atlantic-2",
					"Component":       "snapshotter",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Handler failed: %v", err)
	}

	got := readPeersFromConfigTOML(t, homeDir)
	if got != "node1@10.0.1.1:26656" {
		t.Fatalf("expected peers in config.toml, got %q", got)
	}
}

func TestHandler_SourcesParam_Static(t *testing.T) {
	homeDir := t.TempDir()
	ensureConfigDir(t, homeDir)
	discoverer := NewPeerDiscoverer(homeDir, nil, nil)
	handler := discoverer.Handler()

	err := handler(context.Background(), map[string]any{
		"sources": []any{
			map[string]any{
				"type":      "static",
				"addresses": []any{"abc@1.2.3.4:26656", "def@5.6.7.8:26656"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Handler failed: %v", err)
	}

	got := readPeersFromConfigTOML(t, homeDir)
	if got != "abc@1.2.3.4:26656,def@5.6.7.8:26656" {
		t.Fatalf("expected peers in config.toml, got %q", got)
	}
}

func TestHandler_SourcesParam_UnknownType(t *testing.T) {
	discoverer := NewPeerDiscoverer(t.TempDir(), nil, nil)
	handler := discoverer.Handler()

	err := handler(context.Background(), map[string]any{
		"sources": []any{
			map[string]any{"type": "kubernetes"},
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown source type")
	}
}

func TestHandler_SourcesParam_EmptySources(t *testing.T) {
	discoverer := NewPeerDiscoverer(t.TempDir(), nil, nil)
	handler := discoverer.Handler()

	err := handler(context.Background(), map[string]any{
		"sources": []any{},
	})
	if err == nil {
		t.Fatal("expected error for empty sources")
	}
}

func TestHandler_MissingSources(t *testing.T) {
	discoverer := NewPeerDiscoverer(t.TempDir(), nil, nil)
	handler := discoverer.Handler()

	err := handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing sources param")
	}
}
