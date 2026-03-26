package tasks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestAssembler_FullFlow(t *testing.T) {
	homeDir := t.TempDir()

	configDir := filepath.Join(homeDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "genesis.json"), []byte(`{"chain_id":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s3Objects := &mockS3GetObject{objects: map[string][]byte{
		"genesis/val-0/gentx.json": []byte(`{"gentx":"val0"}`),
		"genesis/val-1/gentx.json": []byte(`{"gentx":"val1"}`),
	}}
	s3Factory := func(_ context.Context, _ string) (S3GetObjectAPI, error) {
		return s3Objects, nil
	}

	var seidCalls [][]string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		seidCalls = append(seidCalls, append([]string{name}, args...))
		return nil, nil
	}

	uploadMock := newMockS3Uploader()

	assembler := NewGenesisAssembler(homeDir, runner, s3Factory, mockUploaderFactory(uploadMock))
	handler := assembler.Handler()

	err := handler(context.Background(), map[string]any{
		"s3Bucket": "my-bucket",
		"s3Prefix": "genesis/",
		"s3Region": "us-west-2",
		"chainId":  "test-chain-1",
		"nodes": []any{
			map[string]any{"name": "val-0"},
			map[string]any{"name": "val-1"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gentxDir := filepath.Join(homeDir, "config", "gentx")
	for _, node := range []string{"val-0", "val-1"} {
		path := filepath.Join(gentxDir, fmt.Sprintf("gentx-%s.json", node))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected gentx file %s to exist", path)
		}
	}

	if len(seidCalls) != 1 {
		t.Fatalf("expected 1 seid call, got %d", len(seidCalls))
	}
	if !contains(seidCalls[0], "collect-gentxs") {
		t.Errorf("expected collect-gentxs command, got %v", seidCalls[0])
	}

	if _, ok := uploadMock.uploads["my-bucket/genesis/genesis.json"]; !ok {
		t.Errorf("expected genesis.json upload to S3, uploads: %v", keys(uploadMock.uploads))
	}
}

func TestAssembler_Idempotent(t *testing.T) {
	homeDir := t.TempDir()
	configDir := filepath.Join(homeDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "genesis.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s3Factory := func(_ context.Context, _ string) (S3GetObjectAPI, error) {
		return &mockS3GetObject{objects: map[string][]byte{
			"p/n/gentx.json": []byte(`{}`),
		}}, nil
	}

	callCount := 0
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		callCount++
		return nil, nil
	}

	assembler := NewGenesisAssembler(homeDir, runner, s3Factory, mockUploaderFactory(newMockS3Uploader()))
	handler := assembler.Handler()

	params := map[string]any{
		"s3Bucket": "b", "s3Prefix": "p/", "s3Region": "r", "chainId": "c",
		"nodes": []any{map[string]any{"name": "n"}},
	}

	if err := handler(context.Background(), params); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 seid call, got %d", callCount)
	}

	if err := handler(context.Background(), params); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if callCount != 1 {
		t.Fatal("expected no new seid calls on second invocation")
	}
}

func TestAssembler_MissingParams(t *testing.T) {
	handler := NewGenesisAssembler(t.TempDir(), nil, nil, nil).Handler()

	tests := []struct {
		name   string
		params map[string]any
	}{
		{"missing bucket", map[string]any{"s3Region": "r", "nodes": []any{map[string]any{"name": "n"}}}},
		{"missing region", map[string]any{"s3Bucket": "b", "nodes": []any{map[string]any{"name": "n"}}}},
		{"missing nodes", map[string]any{"s3Bucket": "b", "s3Region": "r"}},
		{"empty nodes", map[string]any{"s3Bucket": "b", "s3Region": "r", "nodes": []any{}}},
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
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) { return nil, nil }

	handler := NewGenesisAssembler(homeDir, runner, s3Factory, nil).Handler()
	err := handler(context.Background(), map[string]any{
		"s3Bucket": "b", "s3Prefix": "p/", "s3Region": "r", "chainId": "c",
		"nodes": []any{map[string]any{"name": "missing-node"}},
	})
	if err == nil {
		t.Fatal("expected error when S3 download fails")
	}
	if !strings.Contains(err.Error(), "NoSuchKey") {
		t.Errorf("expected NoSuchKey in error, got: %v", err)
	}
}

func TestAssembler_CollectGentxsFailure(t *testing.T) {
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}

	s3Factory := func(_ context.Context, _ string) (S3GetObjectAPI, error) {
		return &mockS3GetObject{objects: map[string][]byte{
			"p/n/gentx.json": []byte(`{}`),
		}}, nil
	}
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("collect-gentxs failed")
	}

	handler := NewGenesisAssembler(homeDir, runner, s3Factory, nil).Handler()
	err := handler(context.Background(), map[string]any{
		"s3Bucket": "b", "s3Prefix": "p/", "s3Region": "r", "chainId": "c",
		"nodes": []any{map[string]any{"name": "n"}},
	})
	if err == nil {
		t.Fatal("expected error when collect-gentxs fails")
	}
}

func TestParseNodeNames(t *testing.T) {
	names, err := parseNodeNames([]any{
		map[string]any{"name": "val-0"},
		map[string]any{"name": "val-1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 2 || names[0] != "val-0" || names[1] != "val-1" {
		t.Errorf("names = %v, want [val-0 val-1]", names)
	}
}

func TestParseNodeNames_MissingName(t *testing.T) {
	_, err := parseNodeNames([]any{
		map[string]any{"other": "field"},
	})
	if err == nil {
		t.Fatal("expected error for missing name field")
	}
}
