# Kubernetes deployment with Helm

`deploy/helm` contains one Helm chart per service (all `version: 0.1.0`,
`appVersion: "0.1.0"`):

| Chart | Workload | Service ports | Notes |
| ----- | -------- | ------------- | ----- |
| `p2pd` | StatefulSet | `p2p` 26656, `grpc` 9091 | PVC via `persistence` (default 10Gi, `ReadWriteOnce`) |
| `consensusd` | StatefulSet | `grpc` 9090, `rpc` 8081 | PVC via `persistence` (default 10Gi); container args `--grpc :9090 --p2p <p2pEndpoint>` |
| `lendingd` | Deployment | `grpc` 50053 | `replicaCount: 0` by default |
| `governd` | Deployment | `grpc` 50061 | |
| `gateway` | Deployment | `http` 8080 | Optional Ingress (`ingress.enabled`, default `false`) |

There is no chart for the swap backend that the gateway requires (see
[Gateway Overview](../gateway/overview.md#backend-endpoints)). Each chart renders a ConfigMap from `config.content`
mounted at `config.path`, and passes `--config <config.path>` to the container.
Images default to `ghcr.io/nhbchain/<service>:<image.tag or appVersion>`.

Per-environment overrides are in `deploy/helm/values/{dev,staging,prod}/<chart>.yaml`.
`deploy/helm/values.yaml` and `deploy/helm/values-prod.yaml` only set
`lendingd.replicaCount: 0`.

## Installing

Use `helm upgrade --install` with the release name, chart directory and the
environment's values file:

```sh
helm upgrade --install p2pd deploy/helm/p2pd -f deploy/helm/values/staging/p2pd.yaml
helm upgrade --install consensusd deploy/helm/consensusd -f deploy/helm/values/staging/consensusd.yaml
helm upgrade --install governd deploy/helm/governd -f deploy/helm/values/staging/governd.yaml
helm upgrade --install gateway deploy/helm/gateway -f deploy/helm/values/staging/gateway.yaml \
  --set secrets.gatewayHMAC="<gateway HMAC secret>"
# lendingd is scaled to zero by default:
helm upgrade --install lendingd deploy/helm/lendingd -f deploy/helm/values/staging/lendingd.yaml \
  --set replicaCount=1
```

Tear down with `helm uninstall <release>`.

`lendingd` is off by default because the chart values say so
(`deploy/helm/lendingd/values.yaml`); the service in `services/lendingd`
registers the lending gRPC handlers, so the chart comment about "preview" does not
describe missing handlers. Its chart config only sets `listen`, while
`services/lendingd/config/config.go` requires a TLS certificate and key (or
`tls.allow_insecure: true`) and at least one API token or mTLS common name, so
extend `config.content` before scaling it up.

The `prod` values for `consensusd` and `p2pd` set `AllowAutogenesis = false`,
`GenesisFile = "/var/lib/nhb/genesis.relaunch.json"` and
`NetworkId = 18346390202490284624` (`deploy/helm/values/prod/`). That file is the
live network's genesis, `config/genesis.relaunch.json`; `cmd/consensusd` writes the
embedded copy of it to the configured path when the file does not exist and
autogenesis is off (`resolveGenesisPath`, `config/embed.go`). A node started from
an empty data directory does not sync the live network from block 1; see
[Onboarding a validator from a snapshot](../validators/snapshot-onboarding.md).

## Secrets

The charts read secrets in two different ways:

- `consensusd`: env `NHB_VALIDATOR_PASS` comes from Kubernetes Secret
  `nhb-validator`, key `password` (`deploy/helm/consensusd/values.yaml`).
- `governd`: env `GOVERND_SIGNER_KEY` comes from Secret `governd-secrets`, key
  `signer-key`, and `GOVERND_TLS_KEY_PATH` is a fixed path. The chart's
  `secrets.*` values are not referenced by any template, so
  `--set secrets.signerKey=...` has no effect. You must create the
  `governd-secrets` Secret yourself.
- `gateway`: `secrets.gatewayHMAC` is templated into the gateway config as
  `auth.hmacSecret`.

`k8s/secrets.example.yaml` creates Secrets named `nhb-validator` (key
`password`), `nhb-governance` (key `signer-key`), `nhb-gateway-auth` (key
`hmac-secret`) and a fourth Secret for an external API key. Only
`nhb-validator` matches a name the charts read. Apply it as a starting point and rename or copy the others to match:

```sh
kubectl apply -f k8s/secrets.example.yaml
```

## Gateway configuration in the chart

The gateway chart's default `config.content` lists `http://` endpoints for
`lendingd`, `governd` and `consensusd`, sets no TLS files and no `NHB_ENV`.
Per [Gateway Overview](../gateway/overview.md#transport-security), the gateway
exits in that state: outside `NHB_ENV=dev` every endpoint must be `https://`
(the auto-upgrade setting does not help) and a TLS certificate and key are
required. The gateway also requires an endpoint for the swap backend, which no chart provides (the built-in default is
`http://127.0.0.1:7102`). Set these through `env`, `config.content` and the
TLS volumes before deploying.

## Ingress

`k8s/ingress.yaml` is a sample manifest for an `nginx` ingress class with a
TLS secret name. It maps one host name to Service `gateway` port 8080 and a
second host name to Service `consensusd` port 8081. Replace the host names and
the TLS secret name in the manifest with your own. The chart's
consensusd config binds `RPCAddress` to `127.0.0.1:8081` inside the pod and
`cmd/consensusd` does not start an HTTP RPC listener, so nothing serves port 8081
on that Service. The gateway chart can also render its own Ingress from
`ingress.hosts`; `values/staging` and `values/prod` enable it.

## Images and chart publishing

`.github/workflows/deploy.yml` (triggers: push to branch `master`, tags `v*`, and
manual dispatch) does the following:

1. Runs `buf lint` and `buf breaking --against '.git#branch=main'`.
2. Builds and pushes images for `gateway`, `consensusd`, `p2pd`, `lendingd` and
   `governd` using `deploy/compose/Dockerfile` to
   `ghcr.io/<repository>/<service>` with tags `type=ref,event=tag`, `type=sha`,
   and `latest` on `master`.
3. Packages and pushes the same five charts to
   `oci://ghcr.io/<repository_owner>/nhb-charts`.

To do the last step by hand:

```sh
helm package deploy/helm/gateway
helm push gateway-0.1.0.tgz oci://ghcr.io/<owner>/nhb-charts
```

## Checking an install

```sh
kubectl get pods -l app.kubernetes.io/instance=<release>
kubectl port-forward svc/gateway 8080:8080
```

The gateway serves `GET /healthz` (returns `ok`). `GET /v1/consensus/status` is
proxied to whatever service the gateway's `consensusd` endpoint points at; no code
in this repository serves that path.
