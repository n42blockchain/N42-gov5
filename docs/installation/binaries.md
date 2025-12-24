# Binaries

Archives of pre-compiled binary files ready for Windows, macOS, and Linux are available. They are static executables. Users of platforms not explicitly listed below should download one of these archives.

## Download Pre-built Binaries

Download the latest release from [GitHub Releases](https://github.com/n42blockchain/N42/releases).

Choose the appropriate binary for your platform:

- **Linux (x86_64)**: `n42-linux-x86_64.tar.gz`
- **Linux (ARM64)**: `n42-linux-aarch64.tar.gz`
- **macOS (Intel)**: `n42-darwin-x86_64.tar.gz`
- **macOS (Apple Silicon)**: `n42-darwin-aarch64.tar.gz`
- **Windows**: `n42-windows-x86_64.zip`

## Installation

### Linux / macOS

```bash
# Download and extract
tar -xzf n42-linux-x86_64.tar.gz

# Move to PATH
sudo mv n42 /usr/local/bin/

# Verify installation
n42 --version
```

### Windows

1. Download and extract the zip file
2. Add the directory containing `n42.exe` to your PATH
3. Verify installation by running `n42 --version` in Command Prompt
