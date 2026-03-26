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
//	  "s3Bucket": "...",
//	  "s3Prefix": "...",
//	  "s3Region": "...",
//	  "chainId":  "...",
//	  "nodes":    [{"name": "node-0"}, {"name": "node-1"}, ...]
//	}
func (a *GenesisAssembler) Handler() engine.TaskHandler {
	return func(ctx context.Context, params map[string]any) error {
		if markerExists(a.homeDir, assembleMarkerFile) {
			assembleLog.Debug("already completed, skipping")
			return nil
		}

		cfg, err := parseAssembleConfig(params)
		if err != nil {
			return err
		}

		if err := a.downloadGentxFiles(ctx, cfg); err != nil {
			return err
		}

		if err := a.addMissingGenesisAccounts(cfg.accountBalance); err != nil {
			return err
		}

		if err := a.collectGentxs(); err != nil {
			return err
		}

		if err := a.uploadGenesis(ctx, cfg); err != nil {
			return err
		}

		assembleLog.Info("genesis assembled and uploaded", "nodes", len(cfg.nodes))
		return writeMarker(a.homeDir, assembleMarkerFile)
	}
}

// downloadGentxFiles fetches each node's gentx.json from S3 and writes
// it to the local config/gentx/ directory.
func (a *GenesisAssembler) downloadGentxFiles(ctx context.Context, cfg assembleConfig) error {
	s3Client, err := a.s3ClientFactory(ctx, cfg.region)
	if err != nil {
		return fmt.Errorf("assemble-genesis: building S3 client: %w", err)
	}

	gentxDir := filepath.Join(a.homeDir, "config", "gentx")
	if err := os.MkdirAll(gentxDir, 0o755); err != nil {
		return fmt.Errorf("assemble-genesis: creating gentx dir: %w", err)
	}

	prefix := normalizePrefix(cfg.prefix)

	for _, nodeName := range cfg.nodes {
		key := fmt.Sprintf("%s%s/gentx.json", prefix, nodeName)
		assembleLog.Info("downloading gentx", "node", nodeName, "key", key)

		output, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(cfg.bucket),
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

		destPath := filepath.Join(gentxDir, fmt.Sprintf("gentx-%s.json", nodeName))
		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			return fmt.Errorf("assemble-genesis: writing %s: %w", destPath, err)
		}
	}

	assembleLog.Info("all gentx files downloaded", "count", len(cfg.nodes))
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

	uploader, err := a.s3UploaderFactory(ctx, cfg.region)
	if err != nil {
		return fmt.Errorf("assemble-genesis: building S3 uploader: %w", err)
	}

	key := normalizePrefix(cfg.prefix) + "genesis.json"
	assembleLog.Info("uploading assembled genesis", "key", key)

	_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(cfg.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("assemble-genesis: uploading genesis.json: %w", err)
	}
	return nil
}

type assembleConfig struct {
	bucket         string
	prefix         string
	region         string
	accountBalance string
	nodes          []string
}

func parseAssembleConfig(params map[string]any) (assembleConfig, error) {
	bucket, _ := params["s3Bucket"].(string)
	prefix, _ := params["s3Prefix"].(string)
	region, _ := params["s3Region"].(string)
	accountBalance, _ := params["accountBalance"].(string)

	if bucket == "" {
		return assembleConfig{}, fmt.Errorf("assemble-genesis: missing required param 's3Bucket'")
	}
	if region == "" {
		return assembleConfig{}, fmt.Errorf("assemble-genesis: missing required param 's3Region'")
	}
	if accountBalance == "" {
		return assembleConfig{}, fmt.Errorf("assemble-genesis: missing required param 'accountBalance'")
	}

	rawNodes, ok := params["nodes"]
	if !ok {
		return assembleConfig{}, fmt.Errorf("assemble-genesis: missing required param 'nodes'")
	}

	nodes, err := parseNodeNames(rawNodes)
	if err != nil {
		return assembleConfig{}, fmt.Errorf("assemble-genesis: %w", err)
	}
	if len(nodes) == 0 {
		return assembleConfig{}, fmt.Errorf("assemble-genesis: 'nodes' list is empty")
	}

	return assembleConfig{bucket: bucket, prefix: prefix, region: region, accountBalance: accountBalance, nodes: nodes}, nil
}

func parseNodeNames(raw any) ([]string, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("'nodes' must be a list, got %T", raw)
	}

	names := make([]string, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("nodes[%d] is not an object", i)
		}
		name, _ := m["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("nodes[%d] missing required field 'name'", i)
		}
		names = append(names, name)
	}
	return names, nil
}
