# Build from Source

You can build N42 on Linux, macOS, Windows, and Windows WSL2.

> **Note**
>
> N42 does not work on Windows WSL1.

## Dependencies

First, install Go (version 1.19 or later) from the [official Go website](https://golang.org/dl/):

```bash
# Linux/macOS
wget https://go.dev/dl/go1.21.0.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.21.0.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin

# Or use your package manager
# macOS: brew install go
# Ubuntu: sudo apt install golang-go
```

## Build N42

With Go and the dependencies installed, you're ready to build N42. First, clone the repository:

```bash
git clone https://github.com/n42blockchain/N42
cd N42
```

Then, build N42:

```bash
make n42
```

The binary will be located at `./build/bin/n42`.

Alternatively, you can build directly with Go:

```bash
go build -o n42 ./cmd/n42
```

Compilation may take several minutes. Run `n42 --help` to verify the installation and see the [command-line documentation](../cli/cli.md).

If you run into any issues, please check the [Troubleshooting](../run/troubleshooting.md) section, or reach out to us on [Telegram](https://t.me/N42_official).

## Update N42

You can update N42 to a specific version by running the commands below.

The N42 directory will be the location you cloned N42 to during the installation process.

`${VERSION}` will be the version you wish to build in the format `vX.X.X`.

```bash
cd N42
git fetch
git checkout ${VERSION}
make n42
```
