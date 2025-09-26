# NHB Examples Workspace

This directory contains self-contained applications and a shared SDK that demonstrate how to interact with the NHB blockchain.

## Prerequisites

- [Node.js](https://nodejs.org/) 18 or newer
- [Yarn](https://classic.yarnpkg.com/lang/en/docs/install/) (v1)

## Getting started

```bash
cd examples
cp .env.example .env
yarn install
yarn dev
```

`yarn dev` starts every application in the workspace. Each app watches the shared `.env` file and exposes a health endpoint.

## Environment

The `.env` file is shared across every app in the workspace. See [docs/examples/overview.md](../docs/examples/overview.md) for an explanation of all variables.

## Workspace layout

```
examples/
  apps/                 # runnable demo applications
  lib-sdk/              # shared JS helpers for NHB RPC and REST
  scripts/              # tooling shared by the workspace
```

## Developing new examples

1. Create a new directory inside `apps/` with its own `package.json`.
2. Add a `dev` script that starts the local UI/server.
3. Update `scripts/dev.js` with the new workspace entry so it is included in `yarn dev`.
4. Import utilities from `lib-sdk` for consistent RPC/HMAC behaviour.

## Running tests

Each workspace package can expose its own `test` script. To run the whole test matrix use:

```bash
yarn test
```

## Troubleshooting

- Ensure the `.env` file exists and contains the gateway credentials.
- Ports are configurable through environment variables (`STATUS_DASHBOARD_PORT`, `NETWORK_MONITOR_PORT`).
- `yarn dev` uses long-running processes; stop them with `Ctrl+C`.
