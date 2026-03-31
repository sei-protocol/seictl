package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	tmcfg "github.com/sei-protocol/sei-chain/sei-tendermint/config"
	tmtypes "github.com/sei-protocol/sei-chain/sei-tendermint/types"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	authtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/auth/types"
	banktypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/bank/types"
	"github.com/sei-protocol/sei-chain/sei-cosmos/x/genutil"
	genutiltypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/genutil/types"
	stakingtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/staking/types"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sei-protocol/seictl/sidecar/engine"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seilog"
)

var assembleLog = seilog.NewLogger("seictl", "task", "assemble-genesis")

const assembleMarkerFile = ".sei-sidecar-assemble-done"

// GenesisAssembler downloads per-node gentx files from S3, calls
// genutil.GenAppStateFromConfig (the same function as seid collect-gentxs)
// to produce the final genesis.json, and uploads it back to S3 for all
// validators to download.
type GenesisAssembler struct {
	homeDir           string
	s3ClientFactory   S3ClientFactory
	s3UploaderFactory seis3.UploaderFactory
}

// assembleNodeEntry represents a single node in the "nodes" list param.
type assembleNodeEntry struct {
	Name string `json:"name"`
}

// assembleConfig holds the typed parameters for the assemble-and-upload-genesis task.
type assembleConfig struct {
	Bucket         string              `json:"s3Bucket"`
	Prefix         string              `json:"s3Prefix"`
	Region         string              `json:"s3Region"`
	AccountBalance string              `json:"accountBalance"`
	Namespace      string              `json:"namespace"`
	Nodes          []assembleNodeEntry `json:"nodes"`
}

// nodeNames returns the list of node name strings from the Nodes entries.
func (c assembleConfig) nodeNames() []string {
	names := make([]string, len(c.Nodes))
	for i, n := range c.Nodes {
		names[i] = n.Name
	}
	return names
}

// NewGenesisAssembler creates an assembler targeting the given home directory.
// The CommandRunner parameter is ignored (kept for call-site compatibility).
func NewGenesisAssembler(homeDir string, _ CommandRunner, s3Factory S3ClientFactory, uploaderFactory seis3.UploaderFactory) *GenesisAssembler {
	if s3Factory == nil {
		s3Factory = DefaultS3ClientFactory
	}
	if uploaderFactory == nil {
		uploaderFactory = seis3.DefaultUploaderFactory
	}
	return &GenesisAssembler{
		homeDir:           homeDir,
		s3ClientFactory:   s3Factory,
		s3UploaderFactory: uploaderFactory,
	}
}

// Handler returns an engine.TaskHandler for the assemble-and-upload-genesis task type.
//
// Expected params:
//
//	{
//	  "s3Bucket":   "...",
//	  "s3Prefix":   "...",
//	  "s3Region":   "...",
//	  "chainId":    "...",
//	  "namespace":  "...",
//	  "accountBalance": "...",
//	  "nodes":      [{"name": "node-0"}, {"name": "node-1"}, ...]
//	}
func (a *GenesisAssembler) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, cfg assembleConfig) error {
		if markerExists(a.homeDir, assembleMarkerFile) {
			assembleLog.Debug("already completed, skipping")
			return nil
		}

		if cfg.Bucket == "" {
			return fmt.Errorf("assemble-genesis: missing required param 's3Bucket'")
		}
		if cfg.Region == "" {
			return fmt.Errorf("assemble-genesis: missing required param 's3Region'")
		}
		if cfg.AccountBalance == "" {
			return fmt.Errorf("assemble-genesis: missing required param 'accountBalance'")
		}
		if cfg.Namespace == "" {
			return fmt.Errorf("assemble-genesis: missing required param 'namespace'")
		}
		if len(cfg.Nodes) == 0 {
			return fmt.Errorf("assemble-genesis: 'nodes' list is empty")
		}
		for i, n := range cfg.Nodes {
			if n.Name == "" {
				return fmt.Errorf("assemble-genesis: nodes[%d] missing required field 'name'", i)
			}
		}

		nodes := cfg.nodeNames()

		if err := a.downloadGentxFiles(ctx, cfg, nodes); err != nil {
			return err
		}

		if err := a.addMissingGenesisAccounts(cfg.AccountBalance); err != nil {
			return err
		}

		if err := a.collectGentxs(); err != nil {
			return err
		}

		if err := a.uploadGenesis(ctx, cfg); err != nil {
			return err
		}

		if err := a.uploadPeers(ctx, cfg, nodes); err != nil {
			return err
		}

		assembleLog.Info("genesis assembled and uploaded", "nodes", len(nodes))
		return writeMarker(a.homeDir, assembleMarkerFile)
	})
}

// downloadGentxFiles fetches each node's gentx.json from S3 and writes
// it to the local config/gentx/ directory.
func (a *GenesisAssembler) downloadGentxFiles(ctx context.Context, cfg assembleConfig, nodes []string) error {
	s3Client, err := a.s3ClientFactory(ctx, cfg.Region)
	if err != nil {
		return fmt.Errorf("assemble-genesis: building S3 client: %w", err)
	}

	gentxDir := filepath.Join(a.homeDir, "config", "gentx")
	if err := os.MkdirAll(gentxDir, 0o755); err != nil {
		return fmt.Errorf("assemble-genesis: creating gentx dir: %w", err)
	}

	prefix := normalizePrefix(cfg.Prefix)

	downloaded := make(map[string]bool, len(nodes))
	for _, nodeName := range nodes {
		key := fmt.Sprintf("%s%s/gentx.json", prefix, nodeName)
		assembleLog.Info("downloading gentx", "node", nodeName, "key", key)

		output, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(cfg.Bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return fmt.Errorf("assemble-genesis: downloading %s: %w", key, err)
		}

		data, err := io.ReadAll(output.Body)
		_ = output.Body.Close()
		if err != nil {
			return fmt.Errorf("assemble-genesis: reading %s: %w", key, err)
		}

		filename := fmt.Sprintf("gentx-%s.json", nodeName)
		destPath := filepath.Join(gentxDir, filename)
		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			return fmt.Errorf("assemble-genesis: writing %s: %w", destPath, err)
		}
		downloaded[filename] = true
	}

	entries, err := os.ReadDir(gentxDir)
	if err != nil {
		return fmt.Errorf("assemble-genesis: reading gentx dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() && !downloaded[e.Name()] {
			_ = os.Remove(filepath.Join(gentxDir, e.Name()))
		}
	}

	assembleLog.Info("all gentx files downloaded", "count", len(nodes))
	return nil
}

// addMissingGenesisAccounts parses each downloaded gentx to extract the
// delegator address, then adds any accounts not already present in the
// assembler's local genesis.json. This is necessary because each node only
// adds its own account during generate-gentx, but collect-gentxs validates
// that every gentx's delegator exists in genesis state.
func (a *GenesisAssembler) addMissingGenesisAccounts(accountBalance string) error {
	cdc, txCfg := makeCodec()
	ensureBech32()

	gentxDir := filepath.Join(a.homeDir, "config", "gentx")
	entries, err := os.ReadDir(gentxDir)
	if err != nil {
		return fmt.Errorf("assemble-genesis: reading gentx dir: %w", err)
	}

	genFile := filepath.Join(a.homeDir, "config", "genesis.json")
	appState, genDoc, err := genutiltypes.GenesisStateFromGenFile(genFile)
	if err != nil {
		return fmt.Errorf("assemble-genesis: reading genesis: %w", err)
	}

	authGenState := authtypes.GetGenesisStateFromAppState(cdc, appState)
	accs, err := authtypes.UnpackAccounts(authGenState.Accounts)
	if err != nil {
		return fmt.Errorf("assemble-genesis: unpacking accounts: %w", err)
	}

	bankGenState := banktypes.GetGenesisStateFromAppState(cdc, appState)

	coins, err := sdk.ParseCoinsNormalized(accountBalance)
	if err != nil {
		return fmt.Errorf("assemble-genesis: parsing account balance: %w", err)
	}

	added := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(gentxDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("assemble-genesis: reading %s: %w", entry.Name(), err)
		}

		tx, err := txCfg.TxJSONDecoder()(data)
		if err != nil {
			return fmt.Errorf("assemble-genesis: decoding %s: %w", entry.Name(), err)
		}

		msgs := tx.GetMsgs()
		if len(msgs) == 0 {
			continue
		}
		msg, ok := msgs[0].(*stakingtypes.MsgCreateValidator)
		if !ok {
			continue
		}

		addr, err := sdk.AccAddressFromBech32(msg.DelegatorAddress)
		if err != nil {
			return fmt.Errorf("assemble-genesis: parsing delegator address from %s: %w", entry.Name(), err)
		}

		if accs.Contains(addr) {
			continue
		}

		accs = append(accs, authtypes.NewBaseAccount(addr, nil, 0, 0))
		bankGenState.Balances = append(bankGenState.Balances, banktypes.Balance{
			Address: addr.String(),
			Coins:   coins.Sort(),
		})
		added++
		assembleLog.Info("added missing genesis account", "address", addr.String())
	}

	if added == 0 {
		return nil
	}

	accs = authtypes.SanitizeGenesisAccounts(accs)
	genAccs, err := authtypes.PackAccounts(accs)
	if err != nil {
		return fmt.Errorf("assemble-genesis: packing accounts: %w", err)
	}
	authGenState.Accounts = genAccs
	authStateBz, err := cdc.MarshalAsJSON(&authGenState)
	if err != nil {
		return fmt.Errorf("assemble-genesis: marshaling auth state: %w", err)
	}
	appState[authtypes.ModuleName] = authStateBz

	bankGenState.Balances = banktypes.SanitizeGenesisBalances(bankGenState.Balances)
	bankStateBz, err := cdc.MarshalAsJSON(bankGenState)
	if err != nil {
		return fmt.Errorf("assemble-genesis: marshaling bank state: %w", err)
	}
	appState[banktypes.ModuleName] = bankStateBz

	appStateJSON, err := json.Marshal(appState)
	if err != nil {
		return fmt.Errorf("assemble-genesis: marshaling app state: %w", err)
	}
	genDoc.AppState = appStateJSON

	if err := genutil.ExportGenesisFile(genDoc, genFile); err != nil {
		return fmt.Errorf("assemble-genesis: writing genesis: %w", err)
	}

	assembleLog.Info("genesis accounts reconciled", "added", added, "total", len(accs))
	return nil
}

// collectGentxs calls genutil.GenAppStateFromConfig — the exact same
// function behind seid collect-gentxs. It decodes each gentx through
// the proto codec, validates balances, extracts persistent peers,
// and writes the final genesis.json.
func (a *GenesisAssembler) collectGentxs() error {
	assembleLog.Info("running collect-gentxs via SDK")

	cdc, txCfg := makeCodec()
	ensureBech32()

	cfg := tmcfg.DefaultConfig()
	cfg.SetRoot(a.homeDir)

	nodeID, valPubKey, err := genutil.InitializeNodeValidatorFiles(cfg)
	if err != nil {
		return fmt.Errorf("assemble-genesis: loading validator files: %w", err)
	}

	genDoc, err := tmtypes.GenesisDocFromFile(cfg.GenesisFile())
	if err != nil {
		return fmt.Errorf("assemble-genesis: reading genesis: %w", err)
	}

	gentxsDir := filepath.Join(a.homeDir, "config", "gentx")
	initCfg := genutiltypes.NewInitConfig(genDoc.ChainID, gentxsDir, nodeID, valPubKey)

	genBalIterator := banktypes.GenesisBalancesIterator{}

	_, err = genutil.GenAppStateFromConfig(cdc, txCfg, cfg, initCfg, *genDoc, genBalIterator)
	if err != nil {
		return fmt.Errorf("assemble-genesis: collect-gentxs: %w", err)
	}

	return nil
}

// uploadGenesis reads the assembled genesis.json and uploads it to S3
// at <prefix>/genesis.json where all validators will fetch it from.
func (a *GenesisAssembler) uploadGenesis(ctx context.Context, cfg assembleConfig) error {
	genesisPath := filepath.Join(a.homeDir, "config", "genesis.json")
	data, err := os.ReadFile(genesisPath)
	if err != nil {
		return fmt.Errorf("assemble-genesis: reading genesis.json: %w", err)
	}

	uploader, err := a.s3UploaderFactory(ctx, cfg.Region)
	if err != nil {
		return fmt.Errorf("assemble-genesis: building S3 uploader: %w", err)
	}

	key := normalizePrefix(cfg.Prefix) + "genesis.json"
	assembleLog.Info("uploading assembled genesis", "key", key)

	_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(cfg.Bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("assemble-genesis: uploading genesis.json: %w", err)
	}
	return nil
}

// uploadPeers builds a peers.json from each node's identity.json and uploads
// it to S3 alongside genesis.json. Each entry is a full Tendermint peer address
// using in-cluster DNS: <nodeID>@<name>-0.<name>.<namespace>.svc.cluster.local:26656
func (a *GenesisAssembler) uploadPeers(ctx context.Context, cfg assembleConfig, nodes []string) error {
	s3Client, err := a.s3ClientFactory(ctx, cfg.Region)
	if err != nil {
		return fmt.Errorf("assemble-genesis: building S3 client for peers: %w", err)
	}

	prefix := normalizePrefix(cfg.Prefix)
	var peers []string

	for _, nodeName := range nodes {
		key := fmt.Sprintf("%s%s/identity.json", prefix, nodeName)
		output, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(cfg.Bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return fmt.Errorf("assemble-genesis: downloading identity for %s: %w", nodeName, err)
		}
		data, err := io.ReadAll(output.Body)
		_ = output.Body.Close()
		if err != nil {
			return fmt.Errorf("assemble-genesis: reading identity for %s: %w", nodeName, err)
		}

		var identity struct {
			NodeKey json.RawMessage `json:"node_key"`
		}
		if err := json.Unmarshal(data, &identity); err != nil {
			return fmt.Errorf("assemble-genesis: parsing identity for %s: %w", nodeName, err)
		}

		var nodeKey struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(identity.NodeKey, &nodeKey); err != nil {
			return fmt.Errorf("assemble-genesis: parsing node_key for %s: %w", nodeName, err)
		}
		if nodeKey.ID == "" {
			return fmt.Errorf("assemble-genesis: empty node ID for %s", nodeName)
		}

		dns := fmt.Sprintf("%s-0.%s.%s.svc.cluster.local", nodeName, nodeName, cfg.Namespace)
		peers = append(peers, fmt.Sprintf("%s@%s:26656", nodeKey.ID, dns))
	}

	peersJSON, err := json.Marshal(peers)
	if err != nil {
		return fmt.Errorf("assemble-genesis: marshaling peers.json: %w", err)
	}

	uploader, err := a.s3UploaderFactory(ctx, cfg.Region)
	if err != nil {
		return fmt.Errorf("assemble-genesis: building S3 uploader for peers: %w", err)
	}

	peersKey := prefix + "peers.json"
	assembleLog.Info("uploading peers.json", "key", peersKey, "count", len(peers))

	_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(cfg.Bucket),
		Key:         aws.String(peersKey),
		Body:        bytes.NewReader(peersJSON),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("assemble-genesis: uploading peers.json: %w", err)
	}
	return nil
}
