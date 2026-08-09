# Pebble-Tool

A command-line tool for reading and modifying HTND PebbleDB databases.

## Overview

`pebble-tool` is a powerful utility for interacting with HTND's PebbleDB database. It provides comprehensive read and write operations, allowing you to inspect, modify, and analyze the database contents.

## Features

- **Read Operations**: Get values by key, scan ranges, dump entire buckets
- **Write Operations**: Put key-value pairs, delete keys
- **Database Analysis**: List all buckets, show statistics, get database info
- **Maintenance**: Compact the database
- **Multiple Output Formats**: Hex, JSON, and text formats supported
- **Prefix Support**: Work with hierarchical bucket structures

## Installation

### From Source

```bash
cd /path/to/HTND/tools/pebble-tool
go build -o pebble-tool .
```

### Copy to System Path

```bash
cp pebble-tool /usr/local/bin/
```

## Usage

### Basic Structure

```bash
pebble-tool -db <database_path> -cmd <command> [options]
```

### Commands

| Command | Description |
|---------|-------------|
| `get` | Get a value by key |
| `put` | Put a key-value pair |
| `delete` | Delete a key |
| `scan` | Scan keys with optional prefix |
| `buckets` | List all buckets in the database |
| `info` | Show database information |
| `compact` | Compact the database |
| `dump` | Dump all keys in a bucket |
| `stats` | Show database statistics |
| `prefixes` | List all prefix paths |

### Common Options

| Option | Description | Default |
|--------|-------------|---------|
| `-db <path>` | Path to PebbleDB database (required) | - |
| `-cache <mb>` | Cache size in MiB | 256 |
| `-bucket <path>` | Bucket path (e.g., 'prefix/block-headers') | - |
| `-key <key>` | Key suffix (hex or string) | - |
| `-value <val>` | Value to put (hex or string) | - |
| `-prefix <pre>` | Prefix for scan operations | - |
| `-limit <n>` | Limit for scan operations | 100 |
| `-format <fmt>` | Output format: hex, json, text | hex |
| `-v` | Verbose output | false |
| `-all` | List all buckets (for buckets command) | false |
| `-max-keys <n>` | Maximum keys to display in dump | 1000 |
| `-out <file>` | Output file path | - |

## Examples

### Get a Value

```bash
# Get a value in hex format (default)
pebble-tool -db /path/to/db -cmd get -bucket "prefix/block-headers" -key "0000000000000000000000000000000000000000000000000000000000000000"

# Get a value in text format
pebble-tool -db /path/to/db -cmd get -bucket "test" -key "mykey" -format text

# Get a value in JSON format (pretty-printed if valid JSON)
pebble-tool -db /path/to/db -cmd get -bucket "config" -key "settings" -format json
```

### Put a Value

```bash
# Put a string value
pebble-tool -db /path/to/db -cmd put -bucket "test" -key "mykey" -value "myvalue"

# Put a hex value
pebble-tool -db /path/to/db -cmd put -bucket "test" -key "hexkey" -value "0102030405"
```

### Delete a Key

```bash
pebble-tool -db /path/to/db -cmd delete -bucket "test" -key "mykey"
```

### Scan Keys

```bash
# Scan all keys in a bucket
pebble-tool -db /path/to/db -cmd scan -bucket "prefix/block-headers" -limit 10

# Scan with a specific prefix
pebble-tool -db /path/to/db -cmd scan -bucket "prefix/block-headers" -prefix "00" -limit 5
```

### Dump All Keys in a Bucket

```bash
# Dump all keys in a bucket (up to 1000 by default)
pebble-tool -db /path/to/db -cmd dump -bucket "utxo-index"

# Dump with higher limit
pebble-tool -db /path/to/db -cmd dump -bucket "utxo-index" -max-keys 5000

# Dump in text format
pebble-tool -db /path/to/db -cmd dump -bucket "test" -format text
```

### List All Buckets

```bash
# Count all unique buckets
pebble-tool -db /path/to/db -cmd buckets

# List all unique buckets
pebble-tool -db /path/to/db -cmd buckets -all
```

### Database Information

```bash
# Show basic database info
pebble-tool -db /path/to/db -cmd info

# Show detailed statistics
pebble-tool -db /path/to/db -cmd stats
```

### Compact Database

```bash
pebble-tool -db /path/to/db -cmd compact
```

### List Prefix-Related Keys

```bash
pebble-tool -db /path/to/db -cmd prefixes
```

## Output Formats

### Hex Format
All bytes are displayed as hexadecimal strings.

### Text Format
Bytes are displayed as UTF-8 text where possible.

### JSON Format
If the value is valid JSON, it will be pretty-printed. Otherwise, it falls back to text format.

## HTND-Specific Usage

### Find Active Prefix

```bash
# Look for active prefix keys
pebble-tool -db /path/to/htnd/data -cmd prefixes
```

### Explore Block Headers

```bash
# List buckets to find block header location
pebble-tool -db /path/to/htnd/data -cmd buckets -all

# Scan block headers (assuming they're in a prefix subdirectory)
pebble-tool -db /path/to/htnd/data -cmd scan -bucket "<prefix>/block-headers" -limit 10 -format text
```

### Get UTXO Information

```bash
# Dump UTXO index
pebble-tool -db /path/to/htnd/data -cmd dump -bucket "utxo-index" -max-keys 100 -format hex
```

## Error Handling

- **Database not found**: Ensure the path exists and is a valid PebbleDB directory
- **Key not found**: The key doesn't exist in the specified bucket
- **Permission denied**: Check file permissions on the database directory
- **Corrupted database**: The tool will attempt to handle corruption, but severe cases may require manual intervention

## Performance Considerations

- Use `-cache` to adjust cache size based on available memory
- For large databases, use `-limit` to restrict scan/dump operations
- The `-max-keys` option prevents accidental dumping of entire large buckets
- Compaction operations (`compact` command) can be resource-intensive

## Security

- **Backup your database** before performing write operations
- The tool has full read/write access to the database
- Use with caution on production databases
- Consider using read-only commands first to verify you're accessing the right data

## Building

```bash
# Build in the tools directory
cd tools/pebble-tool
go build -o pebble-tool .

# Or build from the repository root
cd /path/to/HTND
go build -o tools/pebble-tool ./tools/pebble-tool
```

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Test thoroughly with different database states
5. Submit a pull request

## License

This tool is part of the HTND project and inherits its license.