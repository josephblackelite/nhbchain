# Environment values

Sample per-environment overrides for the NHB Helm charts live in the nested folders:

- `dev/`
- `staging/`
- `prod/`

Each directory has one file per chart: `gateway.yaml`, `consensusd.yaml`, `p2pd.yaml`, `lendingd.yaml` and `governd.yaml`. There is no chart for the swap backend that the gateway requires.
Use them with `helm upgrade --install` by passing the relevant file with `-f`. For example:

```sh
helm upgrade --install gateway deploy/helm/gateway -f deploy/helm/values/staging/gateway.yaml
```

The gateway files template `secrets.gatewayHMAC` into the gateway config, so pass it with `--set secrets.gatewayHMAC=...`. The `governd` chart reads its signer key from the Secret `governd-secrets` (key `signer-key`). See [Kubernetes deployment](../../../docs/deploy/kubernetes.md).
