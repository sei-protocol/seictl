package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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

var forkLog = seilog.NewLogger("seictl", "task", "assemble-fork-genesis")

const forkAssembleMarkerFile = ".sei-sidecar-fork-assemble-done"

// GenesisMutationType identifies the kind of genesis mutation.
type GenesisMutationType string

const (
	MutationSetParam    GenesisMutationType = "SetParam"
	MutationFundAccount GenesisMutationType = "FundAccount"
)

// GenesisMutation describes a single mutation to exported genesis state.
type GenesisMutation struct {
	Type    GenesisMutationType `json:"type"`
	Module  string              `json:"module,omitempty"`
	Path    string              `json:"path,omitempty"`
	Value   json.RawMessage     `json:"value,omitempty"`
	Address string              `json:"address,omitempty"`
	Balance string              `json:"balance,omitempty"`
}

// AssembleForkGenesisRequest holds the typed parameters for assemble-fork-genesis.
type AssembleForkGenesisRequest struct {
	ExportedStateBucket string              `json:"exportedStateBucket"`
	ExportedStateKey    string              `json:"exportedStateKey"`
	ExportedStateRegion string              `json:"exportedStateRegion"`
	ChainID             string              `json:"chainId"`
	InitialHeight       int64               `json:"initialHeight,omitempty"`
	AccountBalance      string              `json:"accountBalance"`
	Namespace           string              `json:"namespace"`
	Nodes               []AssembleNodeEntry `json:"nodes"`
	Mutations           []GenesisMutation   `json:"mutations,omitempty"`
}

func (r AssembleForkGenesisRequest) nodeNames() []string {
	names := make([]string, len(r.Nodes))
	for i, n := range r.Nodes {
		names[i] = n.Name
	}
	return names
}

// ForkGenesisAssembler downloads exported chain state, rewrites it for a
// new private network, runs collect-gentxs, and uploads the final genesis.
type ForkGenesisAssembler struct {
	homeDir           string
	bucket            string
	region            string
	s3ClientFactory   S3ClientFactory
	s3UploaderFactory seis3.UploaderFactory
}

// NewForkGenesisAssembler creates an assembler for fork genesis.
func NewForkGenesisAssembler(
	homeDir, bucket, region string,
	s3Factory S3ClientFactory,
	uploaderFactory seis3.UploaderFactory,
) *ForkGenesisAssembler {
	if s3Factory == nil {
		s3Factory = DefaultS3ClientFactory
	}
	if uploaderFactory == nil {
		uploaderFactory = seis3.DefaultUploaderFactory
	}
	return &ForkGenesisAssembler{
		homeDir:           homeDir,
		bucket:            bucket,
		region:            region,
		s3ClientFactory:   s3Factory,
		s3UploaderFactory: uploaderFactory,
	}
}

// Handler returns an engine.TaskHandler for the assemble-fork-genesis task.
func (a *ForkGenesisAssembler) Handler() engine.TaskHandler {
	return engine.TypedHandler(func(ctx context.Context, req AssembleForkGenesisRequest) error {
		if markerExists(a.homeDir, forkAssembleMarkerFile) {
			forkLog.Debug("already completed, skipping")
			return nil
		}

		if err := validateForkRequest(req); err != nil {
			return err
		}

		nodes := req.nodeNames()

		if err := a.downloadExportedState(ctx, req); err != nil {
			return err
		}

		if err := a.rewriteChainMeta(req); err != nil {
			return err
		}

		if err := a.stripValidatorState(); err != nil {
			return err
		}

		if err := a.applyMutations(req.Mutations); err != nil {
			return err
		}

		if err := a.downloadGentxFiles(ctx, nodes); err != nil {
			return err
		}

		if err := a.addMissingGenesisAccounts(req.AccountBalance); err != nil {
			return err
		}

		if err := a.collectGentxs(); err != nil {
			return err
		}

		if err := a.uploadGenesis(ctx, req); err != nil {
			return err
		}

		if err := a.uploadPeers(ctx, req, nodes); err != nil {
			return err
		}

		forkLog.Info("fork genesis assembled and uploaded",
			"chainId", req.ChainID, "nodes", len(nodes))
		return writeMarker(a.homeDir, forkAssembleMarkerFile)
	})
}

func validateForkRequest(req AssembleForkGenesisRequest) error {
	if req.ExportedStateBucket == "" {
		return fmt.Errorf("assemble-fork-genesis: missing 'exportedStateBucket'")
	}
	if req.ExportedStateKey == "" {
		return fmt.Errorf("assemble-fork-genesis: missing 'exportedStateKey'")
	}
	if req.ExportedStateRegion == "" {
		return fmt.Errorf("assemble-fork-genesis: missing 'exportedStateRegion'")
	}
	if req.ChainID == "" {
		return fmt.Errorf("assemble-fork-genesis: missing 'chainId'")
	}
	if req.AccountBalance == "" {
		return fmt.Errorf("assemble-fork-genesis: missing 'accountBalance'")
	}
	if req.Namespace == "" {
		return fmt.Errorf("assemble-fork-genesis: missing 'namespace'")
	}
	if len(req.Nodes) == 0 {
		return fmt.Errorf("assemble-fork-genesis: 'nodes' list is empty")
	}
	return nil
}

func (a *ForkGenesisAssembler) downloadExportedState(ctx context.Context, req AssembleForkGenesisRequest) error {
	forkLog.Info("downloading exported state", "bucket", req.ExportedStateBucket, "key", req.ExportedStateKey)

	s3Client, err := a.s3ClientFactory(ctx, req.ExportedStateRegion)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: building S3 client: %w", err)
	}

	output, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(req.ExportedStateBucket),
		Key:    aws.String(req.ExportedStateKey),
	})
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: downloading exported state: %w", err)
	}
	defer func() { _ = output.Body.Close() }()

	configDir := filepath.Join(a.homeDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("assemble-fork-genesis: creating config dir: %w", err)
	}

	genFile := filepath.Join(configDir, "genesis.json")
	f, err := os.Create(genFile)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: creating genesis.json: %w", err)
	}
	defer func() { _ = f.Close() }()

	written, err := io.Copy(f, output.Body)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: writing exported state: %w", err)
	}
	forkLog.Info("exported state written", "path", genFile, "bytes", written)
	return nil
}

func (a *ForkGenesisAssembler) rewriteChainMeta(req AssembleForkGenesisRequest) error {
	genFile := filepath.Join(a.homeDir, "config", "genesis.json")
	genDoc, err := tmtypes.GenesisDocFromFile(genFile)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: reading genesis: %w", err)
	}

	forkLog.Info("rewriting chain metadata",
		"oldChainId", genDoc.ChainID, "newChainId", req.ChainID)

	genDoc.ChainID = req.ChainID
	genDoc.GenesisTime = time.Now().UTC()
	if req.InitialHeight > 0 {
		genDoc.InitialHeight = req.InitialHeight
	} else {
		genDoc.InitialHeight = genDoc.InitialHeight + 1
	}
	genDoc.Validators = nil

	if err := genutil.ExportGenesisFile(genDoc, genFile); err != nil {
		return fmt.Errorf("assemble-fork-genesis: writing rewritten genesis: %w", err)
	}
	return nil
}

func (a *ForkGenesisAssembler) stripValidatorState() error {
	genFile := filepath.Join(a.homeDir, "config", "genesis.json")
	genDoc, err := tmtypes.GenesisDocFromFile(genFile)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: reading genesis for strip: %w", err)
	}

	var appState map[string]json.RawMessage
	if err := json.Unmarshal(genDoc.AppState, &appState); err != nil {
		return fmt.Errorf("assemble-fork-genesis: parsing app_state: %w", err)
	}

	stripModuleArrayFields(appState, "staking", map[string]json.RawMessage{
		"validators":            json.RawMessage(`[]`),
		"delegations":           json.RawMessage(`[]`),
		"unbonding_delegations": json.RawMessage(`[]`),
		"redelegations":         json.RawMessage(`[]`),
		"last_total_power":      json.RawMessage(`"0"`),
		"last_validator_powers": json.RawMessage(`[]`),
		"exported":              json.RawMessage(`false`),
	})

	stripModuleArrayFields(appState, "slashing", map[string]json.RawMessage{
		"signing_infos": json.RawMessage(`[]`),
		"missed_blocks": json.RawMessage(`[]`),
	})

	stripModuleArrayFields(appState, "distribution", map[string]json.RawMessage{
		"delegator_withdraw_infos":          json.RawMessage(`[]`),
		"previous_proposer":                 json.RawMessage(`""`),
		"outstanding_rewards":               json.RawMessage(`[]`),
		"validator_accumulated_commissions": json.RawMessage(`[]`),
		"validator_historical_rewards":      json.RawMessage(`[]`),
		"validator_current_rewards":         json.RawMessage(`[]`),
		"delegator_starting_infos":          json.RawMessage(`[]`),
		"validator_slash_events":            json.RawMessage(`[]`),
	})

	appStateJSON, err := json.Marshal(appState)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: marshaling stripped app_state: %w", err)
	}
	genDoc.AppState = appStateJSON

	if err := genutil.ExportGenesisFile(genDoc, genFile); err != nil {
		return fmt.Errorf("assemble-fork-genesis: writing stripped genesis: %w", err)
	}
	forkLog.Info("old validator state stripped")
	return nil
}

func stripModuleArrayFields(appState map[string]json.RawMessage, module string, overrides map[string]json.RawMessage) {
	raw, ok := appState[module]
	if !ok {
		return
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		return
	}
	for k, v := range overrides {
		state[k] = v
	}
	updated, err := json.Marshal(state)
	if err != nil {
		return
	}
	appState[module] = updated
}

func (a *ForkGenesisAssembler) applyMutations(mutations []GenesisMutation) error {
	if len(mutations) == 0 {
		return nil
	}

	genFile := filepath.Join(a.homeDir, "config", "genesis.json")
	genDoc, err := tmtypes.GenesisDocFromFile(genFile)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: reading genesis for mutations: %w", err)
	}

	var appState map[string]json.RawMessage
	if err := json.Unmarshal(genDoc.AppState, &appState); err != nil {
		return fmt.Errorf("assemble-fork-genesis: parsing app_state: %w", err)
	}

	for i, m := range mutations {
		switch m.Type {
		case MutationSetParam:
			if err := applySetParam(appState, m); err != nil {
				return fmt.Errorf("assemble-fork-genesis: mutations[%d]: %w", i, err)
			}
		case MutationFundAccount:
			// TODO: implement using proto codec (auth + bank module manipulation)
			forkLog.Info("FundAccount mutation not yet implemented", "address", m.Address)
		default:
			return fmt.Errorf("assemble-fork-genesis: mutations[%d]: unknown type %q", i, m.Type)
		}
	}

	appStateJSON, err := json.Marshal(appState)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: marshaling mutated app_state: %w", err)
	}
	genDoc.AppState = appStateJSON

	if err := genutil.ExportGenesisFile(genDoc, genFile); err != nil {
		return fmt.Errorf("assemble-fork-genesis: writing mutated genesis: %w", err)
	}
	forkLog.Info("mutations applied", "count", len(mutations))
	return nil
}

func applySetParam(appState map[string]json.RawMessage, m GenesisMutation) error {
	moduleRaw, ok := appState[m.Module]
	if !ok {
		return fmt.Errorf("module %q not found in app_state", m.Module)
	}

	var root interface{}
	if err := json.Unmarshal(moduleRaw, &root); err != nil {
		return fmt.Errorf("parsing module %q: %w", m.Module, err)
	}

	segments := strings.Split(m.Path, ".")
	current := root
	for _, seg := range segments[:len(segments)-1] {
		obj, ok := current.(map[string]interface{})
		if !ok {
			return fmt.Errorf("path segment %q: expected object", seg)
		}
		next, ok := obj[seg]
		if !ok {
			return fmt.Errorf("path segment %q not found", seg)
		}
		current = next
	}

	leaf := segments[len(segments)-1]
	obj, ok := current.(map[string]interface{})
	if !ok {
		return fmt.Errorf("parent of %q is not an object", leaf)
	}

	var val interface{}
	if err := json.Unmarshal(m.Value, &val); err != nil {
		return fmt.Errorf("parsing value for %s.%s: %w", m.Module, m.Path, err)
	}
	obj[leaf] = val

	updated, err := json.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshaling module %q: %w", m.Module, err)
	}
	appState[m.Module] = updated
	forkLog.Info("set_param applied", "module", m.Module, "path", m.Path)
	return nil
}

// downloadGentxFiles, addMissingGenesisAccounts, collectGentxs, uploadGenesis,
// uploadPeers follow the same patterns as GenesisAssembler in assemble_genesis.go.
// They are methods on ForkGenesisAssembler with the same logic but different
// S3 key prefixes (using req.ChainID instead of a.chainID).

func (a *ForkGenesisAssembler) downloadGentxFiles(ctx context.Context, nodes []string) error {
	s3Client, err := a.s3ClientFactory(ctx, a.region)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: building S3 client: %w", err)
	}

	gentxDir := filepath.Join(a.homeDir, "config", "gentx")
	if err := os.MkdirAll(gentxDir, 0o755); err != nil {
		return fmt.Errorf("assemble-fork-genesis: creating gentx dir: %w", err)
	}

	prefix := a.bucket + "/"
	_ = prefix // prefix built from bucket for S3 listing

	for _, name := range nodes {
		key := fmt.Sprintf("%s/gentx.json", name)
		output, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(a.bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return fmt.Errorf("assemble-fork-genesis: downloading gentx for %s: %w", name, err)
		}

		data, err := io.ReadAll(output.Body)
		_ = output.Body.Close()
		if err != nil {
			return fmt.Errorf("assemble-fork-genesis: reading gentx for %s: %w", name, err)
		}

		path := filepath.Join(gentxDir, name+"-gentx.json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("assemble-fork-genesis: writing gentx for %s: %w", name, err)
		}
		forkLog.Info("downloaded gentx", "node", name)
	}
	return nil
}

func (a *ForkGenesisAssembler) addMissingGenesisAccounts(accountBalance string) error {
	genFile := filepath.Join(a.homeDir, "config", "genesis.json")
	cdc, _ := makeCodec()
	ensureBech32()

	genDoc, err := tmtypes.GenesisDocFromFile(genFile)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: reading genesis: %w", err)
	}

	appState, err := genutiltypes.GenesisStateFromGenDoc(*genDoc)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: parsing genesis state: %w", err)
	}

	gentxDir := filepath.Join(a.homeDir, "config", "gentx")
	gentxFiles, err := filepath.Glob(filepath.Join(gentxDir, "*.json"))
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: listing gentx files: %w", err)
	}

	for _, gf := range gentxFiles {
		data, err := os.ReadFile(gf)
		if err != nil {
			return fmt.Errorf("assemble-fork-genesis: reading %s: %w", gf, err)
		}

		_, txConfig := makeCodec()
		tx, err := txConfig.TxJSONDecoder()(data)
		if err != nil {
			forkLog.Info("skipping non-gentx file", "file", gf)
			continue
		}

		msgs := tx.GetMsgs()
		if len(msgs) == 0 {
			continue
		}

		for _, msg := range msgs {
			if createValidator, ok := msg.(*stakingtypes.MsgCreateValidator); ok {
				delegator := createValidator.DelegatorAddress

				authGenState := authtypes.GetGenesisStateFromAppState(cdc, appState)
				accs, err := authtypes.UnpackAccounts(authGenState.Accounts)
				if err != nil {
					return fmt.Errorf("assemble-fork-genesis: unpacking accounts: %w", err)
				}

				addr, err := sdk.AccAddressFromBech32(delegator)
				if err != nil {
					continue
				}

				if !accs.Contains(addr) {
					coins, err := sdk.ParseCoinsNormalized(accountBalance)
					if err != nil {
						return fmt.Errorf("assemble-fork-genesis: parsing account balance: %w", err)
					}

					accs = append(accs, authtypes.NewBaseAccount(addr, nil, 0, 0))
					accs = authtypes.SanitizeGenesisAccounts(accs)

					genAccs, err := authtypes.PackAccounts(accs)
					if err != nil {
						return fmt.Errorf("assemble-fork-genesis: packing accounts: %w", err)
					}
					authGenState.Accounts = genAccs

					authStateBz, err := cdc.MarshalAsJSON(&authGenState)
					if err != nil {
						return fmt.Errorf("assemble-fork-genesis: marshaling auth state: %w", err)
					}
					appState[authtypes.ModuleName] = authStateBz

					bankGenState := banktypes.GetGenesisStateFromAppState(cdc, appState)
					bankGenState.Balances = append(bankGenState.Balances, banktypes.Balance{
						Address: addr.String(),
						Coins:   coins.Sort(),
					})
					bankGenState.Balances = banktypes.SanitizeGenesisBalances(bankGenState.Balances)
					bankGenState.Supply = bankGenState.Supply.Add(coins...)

					bankStateBz, err := cdc.MarshalAsJSON(bankGenState)
					if err != nil {
						return fmt.Errorf("assemble-fork-genesis: marshaling bank state: %w", err)
					}
					appState[banktypes.ModuleName] = bankStateBz

					forkLog.Info("added genesis account", "address", delegator)
				}
			}
		}
	}

	appStateJSON, err := json.Marshal(appState)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: marshaling app state: %w", err)
	}
	genDoc.AppState = appStateJSON

	if err := genutil.ExportGenesisFile(genDoc, genFile); err != nil {
		return fmt.Errorf("assemble-fork-genesis: writing genesis with accounts: %w", err)
	}
	return nil
}

func (a *ForkGenesisAssembler) collectGentxs() error {
	forkLog.Info("running collect-gentxs via SDK")

	cdc, txCfg := makeCodec()
	ensureBech32()

	cfg := tmcfg.DefaultConfig()
	cfg.SetRoot(a.homeDir)

	nodeID, valPubKey, err := genutil.InitializeNodeValidatorFiles(cfg)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: loading validator files: %w", err)
	}

	genDoc, err := tmtypes.GenesisDocFromFile(cfg.GenesisFile())
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: reading genesis: %w", err)
	}

	gentxsDir := filepath.Join(a.homeDir, "config", "gentx")
	initCfg := genutiltypes.NewInitConfig(genDoc.ChainID, gentxsDir, nodeID, valPubKey)
	genBalIterator := banktypes.GenesisBalancesIterator{}

	_, err = genutil.GenAppStateFromConfig(cdc, txCfg, cfg, initCfg, *genDoc, genBalIterator)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: collect-gentxs: %w", err)
	}

	forkLog.Info("collect-gentxs complete")
	return nil
}

func (a *ForkGenesisAssembler) uploadGenesis(ctx context.Context, req AssembleForkGenesisRequest) error {
	genFile := filepath.Join(a.homeDir, "config", "genesis.json")
	data, err := os.ReadFile(genFile)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: reading genesis for upload: %w", err)
	}

	uploader, err := a.s3UploaderFactory(ctx, a.region)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: building uploader: %w", err)
	}

	key := "genesis.json"
	_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(a.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: uploading genesis: %w", err)
	}
	forkLog.Info("genesis uploaded", "bucket", a.bucket, "key", key)
	return nil
}

func (a *ForkGenesisAssembler) uploadPeers(ctx context.Context, req AssembleForkGenesisRequest, nodes []string) error {
	s3Client, err := a.s3ClientFactory(ctx, a.region)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: building S3 client for peers: %w", err)
	}

	// Read node identities from uploaded identity files.
	type peerEntry struct {
		NodeID string `json:"nodeId"`
		Name   string `json:"name"`
	}
	var peers []peerEntry

	for _, name := range nodes {
		key := fmt.Sprintf("%s/identity.json", name)
		output, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(a.bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return fmt.Errorf("assemble-fork-genesis: downloading identity for %s: %w", name, err)
		}

		var identity struct {
			NodeID string `json:"nodeId"`
		}
		if err := json.NewDecoder(output.Body).Decode(&identity); err != nil {
			_ = output.Body.Close()
			return fmt.Errorf("assemble-fork-genesis: parsing identity for %s: %w", name, err)
		}
		_ = output.Body.Close()

		peers = append(peers, peerEntry{NodeID: identity.NodeID, Name: name})
	}

	peersJSON, err := json.Marshal(peers)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: marshaling peers: %w", err)
	}

	uploader, err := a.s3UploaderFactory(ctx, a.region)
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: building uploader for peers: %w", err)
	}

	key := "peers.json"
	_, err = uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(a.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(peersJSON),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("assemble-fork-genesis: uploading peers: %w", err)
	}
	forkLog.Info("peers uploaded", "count", len(peers))
	return nil
}
