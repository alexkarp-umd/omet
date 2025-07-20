# OMET Kubernetes Examples

This directory contains example Kubernetes manifests showing how to deploy OMET as a sidecar container with proper health checks and monitoring.

## Architecture

- **Main App Container**: Your application (nginx in this example)
- **OMET Sidecar**: Collects and processes metrics using OMET
- **Metrics Server**: Nginx serving the metrics file for Prometheus scraping
- **Shared Volume**: EmptyDir volume for sharing metrics between containers

## Health Check Strategy

### Readiness Probe (OMET Sidecar)
- Checks if `omet_last_write` timestamp is recent (< 5 minutes)
- Ensures metrics are being actively updated
- Fails if metrics file is missing or stale

### Liveness Probe (OMET Sidecar)  
- Checks `omet_consecutive_errors_total` metric
- Restarts container if too many consecutive errors (> 10)
- Prevents stuck/degraded OMET processes

## Deployment

```bash
# Create namespace
kubectl create namespace monitoring

# Deploy the application
kubectl apply -f k8s/

# Check pod status
kubectl get pods -n monitoring

# Check metrics endpoint
kubectl port-forward -n monitoring svc/myapp-metrics 8080:8080
curl http://localhost:8080/metrics
```

## Monitoring

The setup includes:
- **ServiceMonitor**: For Prometheus Operator scraping
- **PrometheusRule**: Alerting rules for OMET health
- **Grafana Dashboard**: (see grafana-dashboard.json)

## Key Metrics for Monitoring

- `omet_last_write`: Timestamp of last successful write
- `omet_consecutive_errors_total`: Number of consecutive failed runs  
- `omet_errors_total`: Total errors by type
- `omet_modifications_total`: Total successful operations
- `omet_process_duration_seconds`: Processing time per operation

## Customization

1. **Replace nginx** with your actual application
2. **Modify collect-metrics.sh** for your specific metrics
3. **Adjust health check thresholds** in the ConfigMap
4. **Update resource limits** based on your workload
