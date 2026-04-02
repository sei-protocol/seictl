package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	tmcfg "github.com/sei-protocol/sei-chain/sei-tendermint/config"
	tmtypes "github.com/sei-protocol/sei-chain/sei-tendermint/types"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	authtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/auth/types"
	banktypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/bank/types"
	"github.com/sei-protocol/sei-chain/sei-cosmos/x/genutil"
	genutiltypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/genutil/types"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sei-protocol/seictl/sidecar/engine"
	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seilog"
)

var forkLog = seilog.NewLogger("sidecar", "task", "assemble-genesis-fork")

const forkAssembleMarkerFile = ".sei-sidecar-fork-assemble-done"

// AssembleGenesisForkRequest holds the typed parameters for assemble-genesis-fork.
// The bucket and region come from the assembler's constructor (platform env vars).
// sourceChainId tells the assembler where to find the exported state.
type AssembleGenesisForkRequest struct {
	SourceChainID  string              `json:"sourceChainId"`
	ChainID        string              `json:"chainId"`
	AccountBalance string              `json:"accountBalance"`
	Namespace      string              `json:"namespace"`
	Nodes          []AssembleNodeEntry `json:"nodes"`
}

func (r AssembleGenesisForkRequest) nodeNames() []string {
	names := make([]string, len(r.Nodes))
	for i, n := range r.Nodes {
		names[i] = n.Name
	}
	return names
}

func (r AssembleGenesisForkRequest) validate() error {
	if r.SourceChainID == "" {
		return fmt.Errorf("assemble-genesis-fork: missing 'sourceChainId'")
	}
	if r.ChainID == "" {
		return fmt.Errorf("assemble-genesis-fork: missing 'chainId'")
	}
	if r.AccountBalance == "" {
		return fmt.Errorf("assemble-genesis-fork: missing 'accountBalance'")
	}
	if r.Namespace == "" {
		return fmt.Errorf("assemble-genesis-fork: missing 'namespace'")
	}
	if len(r.Nodes) == 0 {
		return fmt.Errorf("assemble-genesis-fork: 'nodes' list is empty")
	}
	for i, n := range r.Nodes {
		if n.Name == "" {
			return fmt.Errorf("assemble-genesis-fork: nodes[%d] missing 'name'", i)
		}
	}
	return nil
}

// GenesisForkAssembler downloads exported chain state from S3, rewrites
// chain identity, strips old validators, and runs collect-gentxs with
// the new validator set. It follows the same patterns as GenesisAssembler
// but starts from exported state instead of a fresh genesis.
type GenesisForkAssembler struct {
	homeDir           string
	bucket            string
	region            string
	s3ClientFactory   S3ClientFactory
	s3UploaderFactory seis3.UploaderFactory
}

// NewGenesisForkAssembler creates an assembler for fork genesis.
// bucket and region are the platform genesis bucket (from env vars).
func NewGenesisForkAssembler(
	homeDir, bucket, region string,
	s3Factory S3ClientFactory,
	uploaderFactory seis3.UploaderFactory,
) *GenesisForkAssembler {
	if s3Factory == nil {
		s3Factory = DefaultS3ClientFactory
	}
	if uploaderFactory == nil {
		uploaderFactory = seis3.DefaultUploaderFactory
	}
	return &GenesisForkAssembler{
		homeDir:           homeDir,
		bucket:            bucket,
		region:            region,
		s3ClientFactory:   s3Factory,
		s3UploaderFactory: uploaderFactory,
	}
}

// Handler returns an engine.TaskHandler for the assemble-genesis-fork task.
func (a *GenesisForkAssembler) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, req AssembleGenesisForkRequest) error {
		if markerExists(a.homeDir, forkAssembleMarkerFile) {
			forkLog.Debug("already completed, skipping")
			return nil
		}
		if err := req.validate(); err != nil {
			return err
		}
		if err := a.assemble(ctx, req); err != nil {
			return err
		}
		return writeMarker(a.homeDir, forkAssembleMarkerFile)
	})
}

func (a *GenesisForkAssembler) assemble(ctx context.Context, req AssembleGenesisForkRequest) error {
	nodes := req.nodeNames()

	if err := a.downloadExportedState(ctx, req.SourceChainID); err != nil {
		return err
	}
	if err := a.rewriteChainMeta(req.ChainID); err != nil {
		return err
	}
	if err := a.stripValidatorState(); err != nil {
		return err
	}
	if err := a.downloadGentxFiles(ctx, req.ChainID, nodes); err != nil {
		return err
	}
	if err := a.addMissingGenesisAccounts(req.AccountBalance); err != nil {
		return err
	}
	if err := a.collectGentxs(); err != nil {
		return err
	}
	if err := a.uploadGenesis(ctx, req.ChainID); err != nil {
		return err
	}
	if err := a.uploadPeers(ctx, req.ChainID, req.Namespace, nodes); err != nil {
		return err
	}

	forkLog.Info("fork genesis assembled", "chainId", req.ChainID, "nodes", len(nodes))
	return nil
}

func (a *GenesisForkAssembler) downloadExportedState(ctx context.Context, sourceChainID string) error {
	key := sourceChainID + "/exported-state.json"
	forkLog.Info("downloading exported state", "bucket", a.bucket, "key", key)

	s3Client, err := a.s3ClientFactory(ctx, a.region)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: building S3 client: %w", err)
	}

	output, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(a.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: downloading exported state: %w", err)
	}
	defer func() { _ = output.Body.Close() }()

	configDir := filepath.Join(a.homeDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("assemble-genesis-fork: creating config dir: %w", err)
	}

	genFile := filepath.Join(configDir, "genesis.json")
	f, err := os.Create(genFile)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: creating genesis.json: %w", err)
	}
	defer func() { _ = f.Close() }()

	written, err := io.Copy(f, output.Body)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: writing exported state: %w", err)
	}
	forkLog.Info("exported state written", "bytes", written)
	return nil
}

func (a *GenesisForkAssembler) rewriteChainMeta(chainID string) error {
	genFile := filepath.Join(a.homeDir, "config", "genesis.json")
	genDoc, err := tmtypes.GenesisDocFromFile(genFile)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: reading genesis: %w", err)
	}

	forkLog.Info("rewriting chain metadata",
		"oldChainId", genDoc.ChainID, "newChainId", chainID)

	genDoc.ChainID = chainID
	genDoc.GenesisTime = time.Now().UTC()
	genDoc.InitialHeight = genDoc.InitialHeight + 1
	genDoc.Validators = nil

	if err := genutil.ExportGenesisFile(genDoc, genFile); err != nil {
		return fmt.Errorf("assemble-genesis-fork: writing rewritten genesis: %w", err)
	}
	return nil
}

func (a *GenesisForkAssembler) stripValidatorState() error {
	genFile := filepath.Join(a.homeDir, "config", "genesis.json")
	genDoc, err := tmtypes.GenesisDocFromFile(genFile)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: reading genesis for strip: %w", err)
	}

	var appState map[string]json.RawMessage
	if err := json.Unmarshal(genDoc.AppState, &appState); err != nil {
		return fmt.Errorf("assemble-genesis-fork: parsing app_state: %w", err)
	}

	if err := setModuleFields(appState, "staking", map[string]json.RawMessage{
		"validators":            json.RawMessage(`[]`),
		"delegations":           json.RawMessage(`[]`),
		"unbonding_delegations": json.RawMessage(`[]`),
		"redelegations":         json.RawMessage(`[]`),
		"last_total_power":      json.RawMessage(`"0"`),
		"last_validator_powers": json.RawMessage(`[]`),
		"exported":              json.RawMessage(`false`),
	}); err != nil {
		return fmt.Errorf("assemble-genesis-fork: stripping staking: %w", err)
	}

	if err := setModuleFields(appState, "slashing", map[string]json.RawMessage{
		"signing_infos": json.RawMessage(`[]`),
		"missed_blocks": json.RawMessage(`[]`),
	}); err != nil {
		return fmt.Errorf("assemble-genesis-fork: stripping slashing: %w", err)
	}

	if err := setModuleFields(appState, "distribution", map[string]json.RawMessage{
		"delegator_withdraw_infos":          json.RawMessage(`[]`),
		"previous_proposer":                 json.RawMessage(`""`),
		"outstanding_rewards":               json.RawMessage(`[]`),
		"validator_accumulated_commissions": json.RawMessage(`[]`),
		"validator_historical_rewards":      json.RawMessage(`[]`),
		"validator_current_rewards":         json.RawMessage(`[]`),
		"delegator_starting_infos":          json.RawMessage(`[]`),
		"validator_slash_events":            json.RawMessage(`[]`),
	}); err != nil {
		return fmt.Errorf("assemble-genesis-fork: stripping distribution: %w", err)
	}

	if err := setModuleFields(appState, "evidence", map[string]json.RawMessage{
		"evidence": json.RawMessage(`[]`),
	}); err != nil {
		return fmt.Errorf("assemble-genesis-fork: stripping evidence: %w", err)
	}

	appStateJSON, err := json.Marshal(appState)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: marshaling stripped app_state: %w", err)
	}
	genDoc.AppState = appStateJSON

	if err := genutil.ExportGenesisFile(genDoc, genFile); err != nil {
		return fmt.Errorf("assemble-genesis-fork: writing stripped genesis: %w", err)
	}
	forkLog.Info("old validator state stripped")
	return nil
}

// setModuleFields overwrites specific fields in a module's JSON state.
// Returns nil if the module is not present in appState (optional modules).
func setModuleFields(appState map[string]json.RawMessage, module string, overrides map[string]json.RawMessage) error {
	raw, ok := appState[module]
	if !ok {
		return nil
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("parsing %s module state: %w", module, err)
	}
	for k, v := range overrides {
		state[k] = v
	}
	updated, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshaling %s module state: %w", module, err)
	}
	appState[module] = updated
	return nil
}

// downloadGentxFiles downloads per-node gentx files from S3.
// Path convention: {bucket}/{chainId}/{nodeName}/gentx.json
func (a *GenesisForkAssembler) downloadGentxFiles(ctx context.Context, chainID string, nodes []string) error {
	s3Client, err := a.s3ClientFactory(ctx, a.region)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: building S3 client: %w", err)
	}

	gentxDir := filepath.Join(a.homeDir, "config", "gentx")
	if err := os.MkdirAll(gentxDir, 0o755); err != nil {
		return fmt.Errorf("assemble-genesis-fork: creating gentx dir: %w", err)
	}

	for _, name := range nodes {
		key := fmt.Sprintf("%s/%s/gentx.json", chainID, name)
		output, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(a.bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return fmt.Errorf("assemble-genesis-fork: downloading gentx for %s: %w", name, err)
		}
		data, err := io.ReadAll(output.Body)
		_ = output.Body.Close()
		if err != nil {
			return fmt.Errorf("assemble-genesis-fork: reading gentx for %s: %w", name, err)
		}
		path := filepath.Join(gentxDir, name+"-gentx.json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("assemble-genesis-fork: writing gentx for %s: %w", name, err)
		}
		forkLog.Info("downloaded gentx", "node", name)
	}
	return nil
}

// addMissingGenesisAccounts adds auth accounts and bank balances for
// new validators whose addresses aren't in the exported state.
func (a *GenesisForkAssembler) addMissingGenesisAccounts(accountBalance string) error {
	cdc, txCfg := makeCodec()
	ensureBech32()

	genFile := filepath.Join(a.homeDir, "config", "genesis.json")
	appState, genDoc, err := genutiltypes.GenesisStateFromGenFile(genFile)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: reading genesis state: %w", err)
	}

	gentxDir := filepath.Join(a.homeDir, "config", "gentx")
	gentxFiles, err := filepath.Glob(filepath.Join(gentxDir, "*.json"))
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: listing gentx files: %w", err)
	}

	coins, err := sdk.ParseCoinsNormalized(accountBalance)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: parsing account balance: %w", err)
	}

	authGenState := authtypes.GetGenesisStateFromAppState(cdc, appState)
	accs, err := authtypes.UnpackAccounts(authGenState.Accounts)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: unpacking accounts: %w", err)
	}

	bankGenState := banktypes.GetGenesisStateFromAppState(cdc, appState)
	added := 0

	for _, gf := range gentxFiles {
		data, err := os.ReadFile(gf)
		if err != nil {
			return fmt.Errorf("assemble-genesis-fork: reading gentx %s: %w", filepath.Base(gf), err)
		}
		tx, err := txCfg.TxJSONDecoder()(data)
		if err != nil {
			return fmt.Errorf("assemble-genesis-fork: decoding gentx %s: %w", filepath.Base(gf), err)
		}
		for _, msg := range tx.GetMsgs() {
			delegator := extractDelegatorAddress(msg)
			if delegator == "" {
				continue
			}
			addr, err := sdk.AccAddressFromBech32(delegator)
			if err != nil {
				return fmt.Errorf("assemble-genesis-fork: parsing delegator address in %s: %w", filepath.Base(gf), err)
			}
			if accs.Contains(addr) {
				continue
			}

			accs = append(accs, authtypes.NewBaseAccount(addr, nil, 0, 0))
			bankGenState.Balances = append(bankGenState.Balances, banktypes.Balance{
				Address: addr.String(),
				Coins:   coins.Sort(),
			})
			bankGenState.Supply = bankGenState.Supply.Add(coins...)
			added++
			forkLog.Info("added genesis account", "address", delegator)
		}
	}

	if added > 0 {
		accs = authtypes.SanitizeGenesisAccounts(accs)
		genAccs, err := authtypes.PackAccounts(accs)
		if err != nil {
			return fmt.Errorf("assemble-genesis-fork: packing accounts: %w", err)
		}
		authGenState.Accounts = genAccs
		authStateBz, err := cdc.MarshalAsJSON(&authGenState)
		if err != nil {
			return fmt.Errorf("assemble-genesis-fork: marshaling auth state: %w", err)
		}
		appState[authtypes.ModuleName] = authStateBz

		bankGenState.Balances = banktypes.SanitizeGenesisBalances(bankGenState.Balances)
		bankStateBz, err := cdc.MarshalAsJSON(bankGenState)
		if err != nil {
			return fmt.Errorf("assemble-genesis-fork: marshaling bank state: %w", err)
		}
		appState[banktypes.ModuleName] = bankStateBz

		appStateJSON, err := json.Marshal(appState)
		if err != nil {
			return fmt.Errorf("assemble-genesis-fork: marshaling app state: %w", err)
		}
		genDoc.AppState = appStateJSON
		if err := genutil.ExportGenesisFile(genDoc, genFile); err != nil {
			return fmt.Errorf("assemble-genesis-fork: writing genesis: %w", err)
		}
	}
	forkLog.Info("genesis accounts reconciled", "added", added)
	return nil
}

// extractDelegatorAddress pulls the delegator address from a MsgCreateValidator.
func extractDelegatorAddress(msg sdk.Msg) string {
	type delegatorMsg interface {
		GetDelegatorAddress() string
	}
	if dm, ok := msg.(delegatorMsg); ok {
		return dm.GetDelegatorAddress()
	}
	return ""
}

func (a *GenesisForkAssembler) collectGentxs() error {
	forkLog.Info("running collect-gentxs")

	cdc, txCfg := makeCodec()
	ensureBech32()

	cfg := tmcfg.DefaultConfig()
	cfg.SetRoot(a.homeDir)

	nodeID, valPubKey, err := genutil.InitializeNodeValidatorFiles(cfg)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: loading validator files: %w", err)
	}

	genDoc, err := tmtypes.GenesisDocFromFile(cfg.GenesisFile())
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: reading genesis: %w", err)
	}

	gentxsDir := filepath.Join(a.homeDir, "config", "gentx")
	initCfg := genutiltypes.NewInitConfig(genDoc.ChainID, gentxsDir, nodeID, valPubKey)
	genBalIterator := banktypes.GenesisBalancesIterator{}

	_, err = genutil.GenAppStateFromConfig(cdc, txCfg, cfg, initCfg, *genDoc, genBalIterator)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: collect-gentxs: %w", err)
	}
	forkLog.Info("collect-gentxs complete")
	return nil
}

func (a *GenesisForkAssembler) uploadGenesis(ctx context.Context, chainID string) error {
	genFile := filepath.Join(a.homeDir, "config", "genesis.json")
	data, err := os.ReadFile(genFile)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: reading genesis: %w", err)
	}

	uploader, err := a.s3UploaderFactory(ctx, a.region)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: building uploader: %w", err)
	}

	key := chainID + "/genesis.json"
	_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(a.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: uploading genesis: %w", err)
	}
	forkLog.Info("genesis uploaded", "key", key)
	return nil
}

func (a *GenesisForkAssembler) uploadPeers(ctx context.Context, chainID, namespace string, nodes []string) error {
	s3Client, err := a.s3ClientFactory(ctx, a.region)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: building S3 client for peers: %w", err)
	}

	var peers []string
	for _, name := range nodes {
		key := fmt.Sprintf("%s/%s/identity.json", chainID, name)
		output, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(a.bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return fmt.Errorf("assemble-genesis-fork: downloading identity for %s: %w", name, err)
		}
		data, err := io.ReadAll(output.Body)
		_ = output.Body.Close()
		if err != nil {
			return fmt.Errorf("assemble-genesis-fork: reading identity for %s: %w", name, err)
		}

		var identity struct {
			NodeKey json.RawMessage `json:"node_key"`
		}
		if err := json.Unmarshal(data, &identity); err != nil {
			return fmt.Errorf("assemble-genesis-fork: parsing identity for %s: %w", name, err)
		}

		var nodeKey struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(identity.NodeKey, &nodeKey); err != nil {
			return fmt.Errorf("assemble-genesis-fork: parsing node_key for %s: %w", name, err)
		}
		if nodeKey.ID == "" {
			return fmt.Errorf("assemble-genesis-fork: empty node ID for %s", name)
		}

		dns := fmt.Sprintf("%s-0.%s.%s.svc.cluster.local", name, name, namespace)
		peers = append(peers, fmt.Sprintf("%s@%s:26656", nodeKey.ID, dns))
	}

	peersJSON, err := json.Marshal(peers)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: marshaling peers: %w", err)
	}

	uploader, err := a.s3UploaderFactory(ctx, a.region)
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: building uploader for peers: %w", err)
	}

	key := chainID + "/peers.json"
	_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(a.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(peersJSON),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("assemble-genesis-fork: uploading peers: %w", err)
	}
	forkLog.Info("peers uploaded", "count", len(peers))
	return nil
}
