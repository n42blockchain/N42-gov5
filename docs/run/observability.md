# Observability with Prometheus & Grafana

N42 provides a variety of metrics for monitoring your node. To serve them from an HTTP endpoint, add the `--metrics` flag:

```bash
n42 node --metrics --metrics.addr 0.0.0.0 --metrics.port 6060
```

While the node is running, you can use the `curl` command to access the endpoint specified by the `--metrics.port` flag to obtain a text dump of the metrics:

```bash
curl 127.0.0.1:6060/metrics
```

The response is quite descriptive but may be verbose. It represents just a snapshot of the metrics at the time you accessed the endpoint.

To periodically poll the endpoint and print the values (excluding the header text) to the terminal, run the following command in a separate terminal:

```bash
while true; do date; curl -s 127.0.0.1:6060/metrics | grep -Ev '^(#|$)' | sort; echo; sleep 10; done
```

We're making progress! For a visual representation of how these metrics evolve over time (typically in a GUI), follow the next steps.

## Prometheus & Grafana

We will use Prometheus to collect metrics from the endpoint we set up, and Grafana to scrape the metrics from Prometheus and display them on a dashboard.

First, install both Prometheus and Grafana, for instance via Homebrew:

```bash
brew update
brew install prometheus
brew install grafana
```

Then, start the Prometheus and Grafana services:

```bash
brew services start prometheus
brew services start grafana
```

### Configure Prometheus

Create or modify the Prometheus configuration to scrape metrics from your N42 node:

```yaml
# prometheus.yml
global:
  scrape_interval: 15s

scrape_configs:
  - job_name: 'n42'
    static_configs:
      - targets: ['localhost:6060']
```

Depending on your installation, the config file for Prometheus might be located at:
- OSX: `/opt/homebrew/etc/prometheus.yml`
- Linuxbrew: `/home/linuxbrew/.linuxbrew/etc/prometheus.yml`
- Others: `/usr/local/etc/prometheus/prometheus.yml`

### Configure Grafana

Next, open `localhost:3000` in your browser, the default URL for Grafana. The default username and password are both "admin".

After logging in, click on the gear icon in the lower left, and select "Data Sources". Then click on "Add data source", choose "Prometheus" as the type, and in the HTTP URL field, enter `http://localhost:9090`. Click "Save & Test".

Note that `localhost:6060` is the endpoint that N42 exposes for Prometheus to collect metrics from, while Prometheus serves these metrics at `localhost:9090` for Grafana to access.

### Import Dashboard

To set up the dashboard in Grafana:
1. Click on the squares icon in the upper left
2. Select "New" → "Import"
3. Upload a dashboard JSON file or use a dashboard ID

You can find example Grafana dashboards in the N42 repository under `etc/grafana/dashboards/`.

## Key Metrics

| Metric | Description |
|--------|-------------|
| `n42_sync_height` | Current synced block height |
| `n42_peers_connected` | Number of connected peers |
| `n42_txpool_pending` | Pending transactions in pool |
| `n42_rpc_requests_total` | Total RPC requests |
| `n42_db_size_bytes` | Database size in bytes |

## Conclusion

In this guide, we've walked you through starting a node, exposing various log levels, exporting metrics, and finally visualizing those metrics on a Grafana dashboard.

This information is invaluable, whether you're running a home node and want to monitor its performance, or you're a contributor interested in the impact of changes on N42's operations.

[installation]: ../installation/installation.md
