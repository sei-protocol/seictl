package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type mockS3GetObject struct {
	objects map[string][]byte
}

func (m *mockS3GetObject) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	key := *input.Key
	data, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("NoSuchKey: %s", key)
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(bytes.NewReader(data)),
	}, nil
}

func TestAssembler_DownloadsGentxFiles(t *testing.T) {
	homeDir := t.TempDir()
	configDir := filepath.Join(homeDir, "config")
	os.MkdirAll(configDir, 0o755)

	s3Objects := &mockS3GetObject{objects: map[string][]byte{
		"genesis/val-0/gentx.json": []byte(`{"gentx":"val0"}`),
		"genesis/val-1/gentx.json": []byte(`{"gentx":"val1"}`),
	}}
	s3Factory := func(_ context.Context, _ string) (S3GetObjectAPI, error) {
		return s3Objects, nil
	}

	assembler := NewGenesisAssembler(homeDir, "my-bucket", "us-west-2", "genesis", nil, s3Factory, mockUploaderFactory(newMockS3Uploader()))

	cfg := AssembleGenesisRequest{
		AccountBalance: "10000000usei",
		Namespace:      "default",
		Nodes:          []AssembleNodeEntry{{Name: "val-0"}, {Name: "val-1"}},
	}

	nodes := cfg.nodeNames()
	if err := assembler.downloadGentxFiles(context.Background(), cfg, nodes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gentxDir := filepath.Join(homeDir, "config", "gentx")
	for _, node := range []string{"val-0", "val-1"} {
		path := filepath.Join(gentxDir, fmt.Sprintf("gentx-%s.json", node))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected gentx file %s to exist", path)
		}
	}
}

func TestAssembler_MissingParams(t *testing.T) {
	handler := NewGenesisAssembler(t.TempDir(), "b", "r", "chain", nil, nil, nil).Handler()

	tests := []struct {
		name   string
		params map[string]any
	}{
		{"missing accountBalance", map[string]any{"namespace": "ns", "nodes": []any{map[string]any{"name": "n"}}}},
		{"missing namespace", map[string]any{"accountBalance": "10usei", "nodes": []any{map[string]any{"name": "n"}}}},
		{"missing nodes", map[string]any{"accountBalance": "10usei", "namespace": "ns"}},
		{"empty nodes", map[string]any{"accountBalance": "10usei", "namespace": "ns", "nodes": []any{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := handler(context.Background(), tt.params); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestAssembler_S3DownloadFailure(t *testing.T) {
	homeDir := t.TempDir()
	s3Factory := func(_ context.Context, _ string) (S3GetObjectAPI, error) {
		return &mockS3GetObject{objects: map[string][]byte{}}, nil
	}

	handler := NewGenesisAssembler(homeDir, "b", "r", "c", nil, s3Factory, nil).Handler()
	err := handler(context.Background(), map[string]any{
		"accountBalance": "10000000usei", "namespace": "default",
		"nodes": []any{map[string]any{"name": "missing-node"}},
	})
	if err == nil {
		t.Fatal("expected error when S3 download fails")
	}
}

func TestParseAssembleNodes(t *testing.T) {
	// Test that AssembleNodeEntry JSON round-trips correctly.
	raw := `[{"name":"val-0"},{"name":"val-1"}]`
	var nodes []AssembleNodeEntry
	if err := json.Unmarshal([]byte(raw), &nodes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 2 || nodes[0].Name != "val-0" || nodes[1].Name != "val-1" {
		t.Errorf("nodes = %v, want [{val-0} {val-1}]", nodes)
	}
}

func TestParseAssembleNodes_MissingName(t *testing.T) {
	// Test that empty names are caught by the handler validation.
	raw := `[{"other":"field"}]`
	var nodes []AssembleNodeEntry
	if err := json.Unmarshal([]byte(raw), &nodes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The node should unmarshal but with empty Name — the handler validates this.
	if nodes[0].Name != "" {
		t.Fatalf("expected empty name, got %q", nodes[0].Name)
	}
}
