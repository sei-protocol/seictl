package client

import (
	"strings"
	"testing"
)

const (
	validSeiAddr1 = "sei1zg69v7y6hn00qy352euf40x77qfrg4nclsjzp9"
	validSeiAddr2 = "sei140x77qfrg4ncn27dauqjx3t83x4ummcpmrsjjl"
)

func validNonForkTask(accounts []GenesisAccountEntry) AssembleAndUploadGenesisTask {
	return AssembleAndUploadGenesisTask{
		AccountBalance: "1000usei",
		Namespace:      "default",
		Nodes:          []GenesisNodeParam{{Name: "node-0"}},
		Accounts:       accounts,
	}
}

func TestAssembleAndUploadGenesisTask_ValidateAccounts(t *testing.T) {
	cases := []struct {
		name     string
		accounts []GenesisAccountEntry
		wantErr  string
	}{
		{name: "no accounts", accounts: nil},
		{name: "valid single", accounts: []GenesisAccountEntry{{Address: validSeiAddr1, Balance: "1usei"}}},
		{name: "valid multiple", accounts: []GenesisAccountEntry{{Address: validSeiAddr1, Balance: "1usei"}, {Address: validSeiAddr2, Balance: "2usei"}}},
		{name: "missing address", accounts: []GenesisAccountEntry{{Balance: "1usei"}}, wantErr: "missing required field Address"},
		{name: "missing balance", accounts: []GenesisAccountEntry{{Address: validSeiAddr1}}, wantErr: "missing required field Balance"},
		{name: "wrong hrp", accounts: []GenesisAccountEntry{{Address: "cosmos1zg69v7y6hn00qy352euf40x77qfrg4ncjur58y", Balance: "1usei"}}, wantErr: `hrp "cosmos"`},
		{name: "bad checksum", accounts: []GenesisAccountEntry{{Address: "sei1zg69v7y6hn00qy352euf40x77qfrg4nclsjzpz", Balance: "1usei"}}, wantErr: "address"},
		{name: "not bech32", accounts: []GenesisAccountEntry{{Address: "junk", Balance: "1usei"}}, wantErr: "address"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validNonForkTask(tc.accounts).Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("expected nil error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error: got %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestAssembleAndUploadGenesisTask_ToTaskRequest_OmitsEmpty(t *testing.T) {
	req := validNonForkTask(nil).ToTaskRequest()
	if req.Params == nil {
		t.Fatal("Params nil")
	}
	if _, present := (*req.Params)["accounts"]; present {
		t.Errorf("nil accounts should omit field; got: %+v", *req.Params)
	}
}

func TestAssembleAndUploadGenesisTask_ToTaskRequest_SerializesAccounts(t *testing.T) {
	accs := []GenesisAccountEntry{{Address: validSeiAddr1, Balance: "1000usei"}}
	req := validNonForkTask(accs).ToTaskRequest()
	if req.Type != TaskTypeAssembleGenesis {
		t.Errorf("Type: got %q, want %q", req.Type, TaskTypeAssembleGenesis)
	}
	got, ok := (*req.Params)["accounts"].([]interface{})
	if !ok || len(got) != 1 {
		t.Fatalf("accounts: %+v", (*req.Params)["accounts"])
	}
	entry := got[0].(map[string]interface{})
	if entry["address"] != validSeiAddr1 || entry["balance"] != "1000usei" {
		t.Errorf("entry: got %+v", entry)
	}
}
