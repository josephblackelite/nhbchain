# Kubernetes deployment with Helm

The `deploy/helm` directory contains self-contained Helm charts for every
runtime service required in a production NHB stack:

- `p2pd`
- `consensusd`
- `lendingd`
- `swapd`
- `governd`
- `gateway`

Each chart packages:

- a Deployment or StatefulSet
- a Service definition
- optional Ingress (gateway) and ConfigMaps for application configuration
- knobs for persistence, resources, and environment variables

## Pre-requisites

- Kubernetes 1.27+
- Helm 3.12+
- A container registry accessible from the cluster (GHCR by default)
- Secrets populated following `k8s/secrets.example.yaml`
- Ingress controller (e.g. NGINX) if you plan to expose the public endpoints

## Installing charts

All charts are standard Helm packages. Install each component with
`helm upgrade --install` and the relevant values file for your environment.

Example (staging):

```sh
helm upgrade --install p2pd deploy/helm/p2pd -f deploy/helm/values/staging/p2pd.yaml
helm upgrade --install consensusd deploy/helm/consensusd -f deploy/helm/values/staging/consensusd.yaml
helm upgrade --install lendingd deploy/helm/lendingd -f deploy/helm/values/staging/lendingd.yaml
helm upgrade --install swapd deploy/helm/swapd -f deploy/helm/values/staging/swapd.yaml
helm upgrade --install governd deploy/helm/governd -f deploy/helm/values/staging/governd.yaml \
  --set secrets.signerKey="$(kubectl get secret nhb-governance -o jsonpath='{.data.signer-key}' | base64 -d)"
helm upgrade --install gateway deploy/helm/gateway -f deploy/helm/values/staging/gateway.yaml \
  --set secrets.gatewayHMAC="$(kubectl get secret nhb-gateway-auth -o jsonpath='{.data.hmac-secret}' | base64 -d)"
```

> **Tip:** the `deploy/helm/values` directory contains dev/staging/prod samples.
> Adjust secrets and domain names before deploying.

## Ingress & DNS

Use `k8s/ingress.yaml` as a starting point for publishing the public
interfaces. It maps:

- `api.nhbcoin.com` → `gateway`
- `rpc.nhbcoin.net` → `consensusd` (HTTP RPC port)

Update the TLS secret reference and annotations to match your ingress
controller. For mTLS within the cluster consider layering a service mesh and
updating the charts with sidecar injection labels.

## Secrets

Populate the secrets referenced by the charts before installing:

```sh
kubectl apply -f k8s/secrets.example.yaml
```

Customize the placeholder values with production credentials. The example
covers:

- `nhb-validator` – validator keystore password
- `nhb-governance` – hex-encoded private key for governd signing
- `nhb-gateway-auth` – optional gateway HMAC secret
- `nhb-swapd-apis` – external oracle API tokens

## Chart releases & CI

The repository ships with a GitHub Actions workflow (`.github/workflows/deploy.yml`)
that builds container images and pushes packaged charts to GHCR. Trigger it by
pushing to `main` or tagging a release. You can also package and publish
manually:

```sh
helm package deploy/helm/gateway
helm push gateway-0.1.0.tgz oci://ghcr.io/<org>/nhb-charts
```

## Validating a staging install

After installing the staging values:

1. Confirm pods are running: `kubectl get pods -l app.kubernetes.io/instance=p2pd`
2. Forward the gateway service: `kubectl port-forward svc/gateway 8080:8080`
3. Call `/v1/consensus/status` to verify the API proxy is wired up.
4. Inspect consensus height via the RPC ingress: `curl https://rpc.nhbcoin.net/status`

Tear down with `helm uninstall <release>` for each component.
