# Environment values

Sample per-environment overrides for the NHB Helm charts live in the nested folders:

- `dev/`
- `staging/`
- `prod/`

Each directory mirrors the chart names (`gateway`, `consensusd`, `p2pd`, `lendingd`, `swapd`, `governd`).
Use them with `helm upgrade --install` by passing the relevant file with `-f`. For example:

```sh
helm upgrade --install gateway deploy/helm/gateway -f deploy/helm/values/staging/gateway.yaml
```

Override sensitive entries such as signer keys and API tokens before deploying.
