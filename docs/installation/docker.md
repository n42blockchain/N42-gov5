# Docker

There are two ways to obtain a N42 Docker image:

1. [GitHub](#github)
2. [Building it from source](#building-the-docker-image)

Once you have obtained the Docker image, proceed to [Using the Docker
image](#using-the-docker-image).

> **Note**
>
> N42 requires Docker Engine version 20.10.10 or higher due to [missing support](https://docs.docker.com/engine/release-notes/20.10/#201010) for the `clone3` syscall in previous versions.

## GitHub

N42 Docker images for both x86_64 and ARM64 machines are published with every release on GitHub Container Registry.

You can obtain the latest image with:

```bash
docker pull n42blockchain/n42
```

Or a specific version (e.g. v1.0.0) with:

```bash
docker pull n42blockchain/n42:1.0.0
```

You can test the image with:

```bash
docker run --rm n42blockchain/n42:latest --version
```

If you see the latest N42 release version, then you've successfully installed N42 via Docker.

## Building the Docker image

To build the image from source, navigate to the root of the repository and run:

```bash
docker build -t n42:local .
```

The build will likely take several minutes. Once it's built, test it with:

```bash
docker run n42:local --version
```

## Using the Docker image

There are two ways to use the Docker image:
1. [Using Docker](#using-plain-docker)
2. [Using Docker Compose](#using-docker-compose)

### Using Plain Docker

To run N42 with Docker, execute:

```bash
docker run -p 8545:8545 -p 8546:8546 -p 30303:30303 -p 30303:30303/udp \
  -v n42data:/data \
  n42blockchain/n42:latest \
  node --http --http.addr 0.0.0.0
```

The above command will create a container named n42 and a named volume called n42data for data persistence. It will also expose port 30303 TCP and UDP for peering with other nodes and ports 8545/8546 for HTTP/WebSocket RPC.

### Using Docker Compose

To run N42 with Docker Compose, execute the following commands from a shell inside the root directory of this repository:

```bash
docker-compose -f docker-compose.yml up -d
```

The default `docker-compose.yml` file will create three containers:

- n42
- Prometheus
- Grafana

Grafana will be exposed on `localhost:3000` and accessible via default credentials (username and password is `admin`).

## Interacting with N42 inside Docker

To interact with N42, you must first open a shell inside the N42 container by running:

```bash
docker exec -it n42 sh
```

Inside the N42 container, refer to the [CLI docs](../cli/cli.md) documentation to interact with N42.
