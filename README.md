# :control_knobs: `seictl`

> Sei node operators' best friend

A command-line utility for managing Sei blockchain daemon. `seictl` provides tools to patch and modify Sei node
configuration files (TOML) and genesis files (JSON) with ease. More features to be added in due course.

## Features

- **Configuration Management**: Patch Sei daemon configuration files (`app.toml`, `client.toml`, `config.toml`)
- **Genesis Management**: Apply merge patches to genesis JSON files
- **Smart Target Detection**: Automatically detects which configuration file to modify based on patch content
- **Flexible Output**: Write to stdout, a specific file, or modify files in-place
- **Atomic Writes**: Safe file modifications using atomic write operations
- **Merge Patch Algorithm**: Intelligently merges patches with existing configurations

## Installation

### Prerequisites

- Go 1.25.2 or higher

### Build from Source

```bash
git clone https://github.com/sei-protocol/seictl.git
go build -o seictl
```

### Install

```bash
go install github.com/sei-protocol/seictl@latest
```

## Usage

```
seictl [global options] command [command options] [arguments...]
```

### Global Options

- `--home <path>`: Sei home directory (default: `~/.sei`, can be set via `SEI_HOME` environment variable)

## Commands

### Genesis Commands

#### `genesis patch`

Apply a merge-patch to the Sei genesis JSON file.

```bash
seictl genesis patch [patch-file]
```

**Options:**

- `-o, --output <path>`: Write output to specified file
- `-i, --in-place-rewrite`: Modify the genesis file in-place

**Examples:**

```bash
# Patch from file and output to stdout
seictl genesis patch patch.json

# Patch from stdin
echo '{"chain_id": "sei-testnet"}' | seictl genesis patch

# Patch and save to a new file
seictl genesis patch patch.json -o genesis-modified.json

# Patch and modify the original file in-place
seictl genesis patch patch.json -i
```

### Config Commands

#### `config patch`

Apply a merge-patch to a Sei configuration TOML file.

```bash
seictl config [--target <app|client|config>] patch [patch-file]
```

**Options:**

- `--target <type>`: Specify which configuration file to patch (`app`, `client`, or `config`)
    - If not specified, the target is automatically detected based on the patch content
- `-o, --output <path>`: Write output to specified file
- `-i, --in-place-rewrite`: Modify the configuration file in-place

**Examples:**

```bash
# Patch with auto-detection
seictl config patch patch.toml

# Explicitly specify the target config
seictl config --target app patch patch.toml

# Patch from stdin and output to stdout
echo 'minimum-gas-prices = "0.01usei"' | seictl config patch

# Patch and modify in-place
seictl config --target app patch patch.toml -i

# Patch and save to specific location
seictl config patch patch.toml -o /path/to/output.toml
```

## Configuration Targets

The `config` command can work with three different configuration files:

### `app.toml`

Application-level configuration including:

- Gas prices and block settings
- State management (state-sync, state-commit, state-store)
- EVM configuration
- Telemetry and monitoring
- API, gRPC, and Rosetta endpoints
- IAVL and WASM settings

### `client.toml`

Client-level configuration including:

- Chain ID
- Keyring backend
- Output format
- Node endpoint
- Broadcast mode

### `config.toml`

Node-level configuration including:

- Proxy app and database settings
- Logging configuration
- RPC and P2P settings
- Mempool and consensus parameters
- State sync and block sync
- Transaction indexing

## Merge Patch Behavior

The merge patch algorithm works as follows:

1. **Nested merging**: Patches are merged recursively into nested structures
2. **Null deletion**: Setting a value to `null` removes that key from the configuration
3. **Addition**: New keys in the patch are added to the configuration
4. **Replacement**: Existing scalar values are replaced with patch values

**Example:**

Original:

```toml
[api]
enable = true
address = "tcp://0.0.0.0:1317"
```

Patch:

```toml
[api]
address = "tcp://0.0.0.0:1318"
swagger = true
```

Result:

```toml
[api]
enable = true
address = "tcp://0.0.0.0:1318"
swagger = true
```

## Examples

### Update Minimum Gas Prices

```bash
echo 'minimum-gas-prices = "0.02usei"' | seictl config patch -i
```

### Enable API Endpoint

```bash
cat > patch.toml << EOF
[api]
enable = true
address = "tcp://0.0.0.0:1317"
EOF

seictl config --target app patch patch.toml -i
```

### Modify Genesis Chain ID

```bash
echo '{"chain_id": "sei-mainnet-1"}' | seictl genesis patch -i
```

### Update Multiple Configuration Sections

```bash
cat > patch.toml << EOF
minimum-gas-prices = "0.01usei"

[telemetry]
enabled = true
prometheus-retention-time = 60

[api]
enable = true
swagger = true
EOF

seictl config patch patch.toml -i
```

### Using Custom Sei Home Directory

```bash
# Via environment variable
export SEI_HOME=/custom/path/.sei
seictl config patch patch.toml

# Via flag
seictl --home /custom/path/.sei config patch patch.toml
```

## File Locations

By default, `seictl` looks for configuration files in the following locations:

- Genesis: `$HOME/.sei/config/genesis.json`
- App config: `$HOME/.sei/config/app.toml`
- Client config: `$HOME/.sei/config/client.toml`
- Node config: `$HOME/.sei/config/config.toml`

Where `$HOME` is the value of the `--home` flag or the `SEI_HOME` environment variable.

## Safety Features

- **Atomic Writes**: All file modifications use atomic write operations (write to temp file, then rename)
- **Permission Preservation**: In-place modifications preserve original file permissions
- **Target Validation**: Prevents accidental modification of wrong configuration files
- **Auto-detection Safety**: Refuses to proceed if patch could apply to multiple targets

## License

This project is licensed under the Apache License 2.0. See the [LICENSE](LICENSE.md) file for details.
