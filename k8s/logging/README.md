# Logging Stack — Kubernetes Manifests

Deploys **Loki** (log storage) and **Promtail** (log collector DaemonSet) into the `monitoring` namespace on the AKS cluster.

## Prerequisites

```bash
# Create the monitoring namespace if it doesn't exist yet
kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f -
```

## Deploy

```bash
# Deploy Loki
kubectl apply -f k8s/logging/loki.yml

# Deploy Promtail (RBAC + DaemonSet)
kubectl apply -f k8s/logging/promtail.yml
```

## Verify

```bash
# Loki should become ready within ~30s
kubectl rollout status deployment/loki -n monitoring

# One Promtail pod per node
kubectl get pods -n monitoring -l app=promtail

# Tail Promtail logs to confirm it is shipping to Loki
kubectl logs -n monitoring -l app=promtail --tail=20
```

## Add Loki datasource to Grafana

If Grafana is deployed in the cluster, patch its provisioning ConfigMap to add:

```yaml
datasources:
  - name: Loki
    type: loki
    access: proxy
    url: http://loki.monitoring.svc.cluster.local:3100
    isDefault: false
    editable: true
    jsonData:
      maxLines: 1000
```

Or use Grafana's UI: **Configuration → Data Sources → Add → Loki → URL: `http://loki.monitoring.svc.cluster.local:3100`**.

## Query logs in Grafana

Open **Explore**, select the **Loki** datasource, and use LogQL:

```logql
# All logs from a specific service across both instances
{service="api-gateway"}

# Error logs across the whole cluster
{namespace=~"project-exbanka-instance."} | json | level="ERROR"

# Peer-banks request trace
{service="api-gateway"} | json | path="/api/v3/peer-banks"

# Logs for a specific request ID
{namespace=~"project-exbanka-instance."} | json | request_id="<uuid>"
```
