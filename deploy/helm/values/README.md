# Environment values

Sample per-environment overrides for the NHB Helm charts live in the nested folders:

- `dev/`
- `staging/`
- `prod/`

Each directory has one file per chart: `gateway.yaml`, `consensusd.yaml`, `p2pd.yaml`, `lendingd.yaml` and `governd.yaml`. There is no chart for the swap backend that the gateway requires.
Use them with `helm upgrade --install` by passing the relevant file with `-f`. For example:

```sh
helm upgrade --install gateway deploy/helm/gateway -f deploy/helm/values/staging/gateway.yaml \
  --set 'trustedProxies={<ingress controller address range>}'
```

The gateway files template `secrets.gatewayHMAC` into the gateway config, so pass it with `--set secrets.gatewayHMAC=...`.
The `staging` and `prod` gateway files enable an ingress and leave `trustedProxies` empty, because the ingress controller's address range depends on the cluster. The gateway chart refuses to render until it is filled in, either in the values file or with `--set`; without it every client behind the ingress would share one rate-limit bucket per ingress pod (see [Gateway rate limits behind a proxy](../../../docs/ops/security.md#gateway-rate-limits-behind-a-proxy)). The `governd` chart reads its signer key from the Secret `governd-secrets` (key `signer-key`). See [Kubernetes deployment](../../../docs/deploy/kubernetes.md).
