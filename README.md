# ClickHouse Diagnostic Tool

A simplified Go-based diagnostic tool for ClickHouse databases that collects system information, executes diagnostic queries, and packages everything into a convenient archive for analysis.

## Features

- **Version-aware query execution**: Automatically detects ClickHouse server version and runs compatible queries
- **Configuration collection**: Safely collects configuration files with password sanitization
- **Structured query organization**: Supports versioned query directories for different ClickHouse versions
- **Secure credential handling**: Prompts for sensitive information and sanitizes passwords from config files
- **Archive creation**: Packages all results and configurations into a compressed tar.gz file
- **Flexible deployment**: Works with both HTTP and HTTPS connections

## Installation

### Prerequisites

- Go 1.21 or later
- Access to a ClickHouse server
- Read access to ClickHouse configuration directory (optional)

## Usage

### Basic Usage

Run the tool interactively (recommended for first-time use):

```bash
./clickhouse-diagnostic
```

The tool will prompt you for:
- Protocol (http/https)
- Host (default: localhost)
- Port (default: 8123 for http, 8443 for https)
- Username
- Password (hidden input)
- Configuration directory path

### Command Line Options

```bash
./clickhouse-diagnostic [options]

Options:
  -host string
        ClickHouse Host
  -port string
        ClickHouse Port
  -user string
        Username
  -password string
        Password (not recommended for security reasons)
  -protocol string
        Protocol (http or https)
  -queries-dir string
        Directory containing query files (default "./queries")
  -output-dir string
        Directory for results output (default "./clickhouse_results")
  -config-dir string
        ClickHouse config directory to collect (default "/etc/clickhouse-server/config.d/")
  -skip-config
        Skip collecting configuration files
  -skip-archive
        Skip creating archive of results and configuration
```

### Example Usage

```bash
# Run with specific host and skip configuration collection
./clickhouse-diagnostic -host production-ch-server -skip-config

# Run with custom queries directory and output location
./clickhouse-diagnostic -queries-dir ./my-queries -output-dir ./diagnostics

# Run with all parameters specified (not recommended for password)
./clickhouse-diagnostic -host localhost -port 8123 -user admin -protocol http
```

## Query Organization

### Root Level Queries

Place general diagnostic queries directly in the `queries/` directory. These queries will be executed regardless of the ClickHouse version.

Example `queries/basic_info.sql`:
```sql
SELECT 
    version(),
    uptime(),
    formatReadableSize(total_memory_usage) as memory_usage,
    formatReadableSize(total_bytes) as disk_usage
FROM system.metrics
```

### Version-Specific Queries

Create subdirectories named with ClickHouse version numbers (format: `MAJOR.MINOR.PATCH.BUILD`) for version-specific queries.

### Query Priority System

If multiple versions of the same query file exist, the tool will select the highest compatible version:

```
queries/
├── performance.sql          # Version 1 (root level)
├── 21.8.0.0/
│   └── performance.sql      # Version 2 (used if server >= 21.8.0.0)
└── 22.3.0.0/
    └── performance.sql      # Version 3 (used if server >= 22.3.0.0)
```

## Configuration Collection

The tool automatically collects ClickHouse configuration files and sanitizes them for security:

### Security Features

- **Password Removal**: Automatically removes passwords from XML configuration files
- **Token Sanitization**: Removes authentication tokens and secrets
- **URL Credential Scrubbing**: Removes credentials from database connection URLs

### Sanitized Elements

The following XML elements and attributes are sanitized:
- `<password>`, `<password_sha256_hex>`, `<password_double_sha1_hex>`, `<password_sha1_hex>`
- `<secret>`, `<token>`
- Attributes: `password="..."`, `secret="..."`, `token="..."`
- URL credentials in connection strings

## Output Structure

After execution, the tool creates:

### Results Directory
```
clickhouse_results/
└── clickhouse_backup_YYYYMMDD_HHMMSS/
    ├── basic_info_YYYYMMDD_HHMMSS.native
    ├── performance_YYYYMMDD_HHMMSS.native
    └── system_tables_YYYYMMDD_HHMMSS.native
```

### Configuration Directory
```
configuration/
├── config.xml
├── users.xml
└── other_config_files.xml
```

### Archive File
```
clickhouse_backup_YYYYMMDD_HHMMSS.tar.gz
├── clickhouse_backup_YYYYMMDD_HHMMSS/    # Query results
└── configuration/                        # Sanitized config files
```

## File Formats

- **Query Results**: Saved in ClickHouse native format (`.native` extension)
- **Configuration**: Original XML format with sanitized credentials
- **Archive**: Compressed tar.gz format for easy sharing

## Error Handling

The tool handles various error conditions gracefully:

- **Connection Issues**: Reports connection problems with details
- **Missing Directories**: Creates required directories or skips if source doesn't exist
- **Invalid Queries**: Logs errors but continues with remaining queries
- **Version Parsing**: Skips directories with invalid version formats
- **File Permissions**: Reports permission issues and continues

## Security Considerations

### Best Practices

1. **Don't use `-password` flag**: Always let the tool prompt for passwords
2. **Review configurations**: Check sanitized config files before sharing
3. **Secure archives**: Treat output archives as containing sensitive system information
4. **Network security**: Use HTTPS when possible for production environments

### What Gets Sanitized

- Database passwords and authentication tokens
- Connection string credentials
- Secret keys and API tokens
- Custom authentication configurations

### What Doesn't Get Sanitized

- Server hostnames and IP addresses
- Database names and table structures
- Performance metrics and statistics
- System configuration (non-security related)

## Troubleshooting

### Common Issues

**"Queries folder does not exist"**
- Create the `queries/` directory and add your diagnostic SQL files

**"Config directory does not exist"**
- Use `-skip-config` flag or provide correct path to ClickHouse config directory

**"Connection refused"**
- Verify ClickHouse server is running and accessible
- Check host, port, and protocol settings
- Verify firewall and network connectivity

**"Permission denied"**
- Ensure read access to configuration directories
- Check write permissions for output directory

