#!/bin/bash
set -e

echo "Updating system..."
sudo apt-get update
sudo DEBIAN_FRONTEND=noninteractive apt-get upgrade -y
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y build-essential curl git wget jq unzip software-properties-common nginx mysql-server

echo "Installing Go..."
GO_VERSION="1.23.1"
wget https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go${GO_VERSION}.linux-amd64.tar.gz
rm go${GO_VERSION}.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile
export PATH=$PATH:/usr/local/go/bin

echo "Installing Node.js v20..."
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y nodejs
sudo npm install -g pm2 yarn

echo "Configuring MySQL..."
sudo systemctl enable mysql
sudo systemctl start mysql

echo "Creating directory structure..."
sudo mkdir -p /var/www/nhbcoin/chain
sudo mkdir -p /var/www/nhbcoin/api
sudo mkdir -p /var/www/nhbcoin/frontend
sudo chown -R $USER:$USER /var/www/nhbcoin

echo "Setup completed successfully!"
