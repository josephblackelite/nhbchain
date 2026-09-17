#!/bin/bash
if [ -z "${NHB_RPC_JWT_SECRET:-}" ] || [ -z "${NHB_VALIDATOR_PASS:-}" ]; then
  echo "Error: NHB_RPC_JWT_SECRET and NHB_VALIDATOR_PASS must both be set in the environment before running this script."
  exit 1
fi
source ~/.profile
cd /home/ubuntu/nhbchain
git reset --hard
git pull origin main
export PATH=$PATH:/usr/local/go/bin
go build -o bin/nhb ./cmd/nhb
pkill -f nhb
sleep 2
nohup env NHB_RPC_JWT_SECRET="${NHB_RPC_JWT_SECRET}" NHB_VALIDATOR_PASS="${NHB_VALIDATOR_PASS}" NHB_ENV="prod" ./bin/nhb --config config.toml > node.log 2>&1 &
