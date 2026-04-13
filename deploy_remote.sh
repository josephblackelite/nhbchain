#!/bin/bash
source ~/.profile
cd /home/ubuntu/nhbchain
git reset --hard
git pull origin main
export PATH=$PATH:/usr/local/go/bin
go build -o bin/nhb ./cmd/nhb
pkill -f nhb
sleep 2
nohup env NHB_RPC_JWT_SECRET="nhb-master-admin-secret-2026!" NHB_VALIDATOR_PASS="nhbmaster2026" NHB_ENV="prod" ./bin/nhb --config config.toml > node.log 2>&1 &
