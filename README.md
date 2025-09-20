NHBCoin L1 Node 🚀
Welcome to the official Go implementation of the NHBCoin Layer 1 blockchain. This repository contains the full source code for running a node on the NHBCoin network, a next-generation payment rail designed for simplicity, security, and mainstream adoption.

      _   _ _____ _____ ____  ____   ___  _   _ _____
     | \ | | ____|_   _| __ )|  _ \ / _ \| \ | |_   _|
     |  \| |  _|   | | |  _ \| | | | | | |  \| | | |
     | |\  | |___  | | | |_) | |_| | |_| | |\  | | |
     |_| \_|_____| |_| |____/|____/ \___/|_| \_| |_|


NHBCoin is engineered with a singular purpose: to serve as the world's most efficient, secure, and accessible payment rail. Unlike general-purpose blockchains, every component of our L1 is optimized for financial transactions and a seamless user experience, abstracting away the complexities of crypto to deliver a service that feels as intuitive as the best modern FinTech applications.

✨ Key Features
The NHBCoin L1 is not just another blockchain. It's a purpose-built platform with powerful features integrated directly into the protocol.

⚡ Proof of Time Spent Online (POTSO): A unique BFT consensus mechanism where a validator's chance to produce blocks is weighted by both their financial Stake and their on-chain Engagement Score. This rewards active participation, not just capital.

** Seamless Account Abstraction (NAA):** Every account is a smart contract account by default. Our native Paymaster model allows transaction fees to be sponsored, enabling a true "gasless" experience for end-users.

🏦 Native Dual-Token System: The protocol natively manages two tokens: NHBCoin, the primary payment stablecoin, and ZapNHB, the loyalty and staking token.

🔐 Native P2P Escrow: A trustless escrow system is built into the chain's logic, enabling a secure P2P marketplace without the need for complex smart contracts.

🆔 Native Identity: Users can claim unique, human-readable usernames that are securely linked to their on-chain address, preventing "wrong address" errors.

🚀 EVM Compatibility: The node includes an integrated Go Ethereum (Geth) EVM, making the chain instantly compatible with the entire ecosystem of Solidity smart contracts, dApps, and developer tools.

🏛️ Architectural Overview
The NHBCoin L1 is constructed from four distinct, interconnected layers that work in concert.

Consensus Layer: The BFT engine powered by our custom POTSO algorithm.

Execution Layer: A hybrid engine that uses our efficient Go code for native features and the Geth EVM for transfers and smart contracts.

Application Layer: The native modules for Identity, Escrow, Loyalty, and more.

Networking Layer: A robust peer-to-peer network with automatic chain synchronization.

🚀 Getting Started: Running Your Own Node
Follow these instructions to clone, build, and run your own NHBCoin node, connecting it to the network.

Prerequisites
Go: Version 1.18 or higher.

Git: For cloning the repository.

An Ubuntu 22.04 LTS server is recommended for deployment.

1. Installation
First, connect to your server and install the necessary dependencies.

# Update package lists
sudo apt update

# Install Git and other build tools
sudo apt install git build-essential -y

# Install the Go programming language
sudo snap install go --classic

2. Clone and Build
Next, clone the repository and compile the node software into a single executable.

# Clone the repository from GitHub
git clone [https://github.com/josephblackelite/nhbchain.git](https://github.com/josephblackelite/nhbchain.git)

# Navigate into the project directory
cd nhbchain

# Download all the necessary Go dependencies
go mod tidy

# Build the node executable
go build -o nhb-node ./cmd/nhb/

# Build the Command-Line Interface (CLI) tool
go build -o nhb-cli ./cmd/nhb-cli/

You will now have two new files: nhb-node (the blockchain client) and nhb-cli (the tool to interact with it).

3. Configuration
The node is configured using a simple config.toml file.

On your very first run, the node will automatically generate a config.toml file for you with a new, unique private key. You can also create one manually:

config.toml

# NHBCoin Node Configuration File

# P2P: Use 0.0.0.0 to listen for connections from the internet
ListenAddress = "0.0.0.0:6001"

# RPC: Use 0.0.0.0 to allow the CLI to connect from anywhere
RPCAddress = "0.0.0.0:8080"

# Path to the blockchain database
DataDir = "./nhb-data"

# On first run, leave this blank. The node will generate and save a key here.
ValidatorKey = ""

# A list of trusted nodes to connect to on startup
BootstrapPeers = [
    # Add the public IP of the main network's bootstrap node here
    # e.g., "123.45.67.89:6001"
]

4. Running the Node
To ensure your node runs 24/7, we recommend using a terminal multiplexer like tmux.

# Install tmux
sudo apt install tmux -y

# Start a new persistent session named "node"
tmux new -s node

# Inside the tmux session, start your node
./nhb-node

The node will start, print its initialization logs, and then run silently. To detach from the session (leaving the node running in the background), press Ctrl+B, release the keys, and then press D.

You can re-attach to the session at any time with tmux attach -t node.

🛠️ Using the CLI (nhb-cli)
The nhb-cli is your command center for interacting with a running node.

Note: For commands that require a private key (stake, heartbeat, etc.), first generate a key and ensure the resulting wallet.key file is in the same directory.

# Generate a new wallet and save it to wallet.key
./nhb-cli generate-key
> Generated new key and saved to wallet.key
> Your public address is: nhb1...

# Check the balance of any address
./nhb-cli balance nhb1...
> State for: nhb1...
>   Username:
>   NHBCoin:  0
>   ZapNHB:   0
>   Staked:   0 ZapNHB
>   Nonce:    0

# Stake 5000 ZapNHB to become a validator candidate
./nhb-cli stake 5000 wallet.key
> Successfully sent stake transaction for 5000 ZapNHB.

# Un-stake 1000 ZapNHB
./nhb-cli un-stake 1000 wallet.key
> Successfully sent un-stake transaction for 1000 ZapNHB.

# Send a heartbeat transaction to boost your Engagement Score
./nhb-cli heartbeat wallet.key
> Successfully sent heartbeat transaction.

# Deploy a smart contract
./nhb-cli deploy ./path/to/contract.hex wallet.key
> Successfully sent contract deployment transaction.

🗺️ Roadmap
The core L1 is feature-complete. Our focus has now shifted to:

Security Hardening: Rigorous internal testing and multiple external security audits.

Frontend Development: Building the beautiful, Venmo-style nhbcoin.com web application and wallet.

Testnet Expansion: Growing the public testnet with more community validators.

Mainnet Launch: The final public release of the NHBCoin network.

🤝 Contributing
We welcome contributions from the community! If you'd like to help, please feel free to:

Open an issue to report a bug or suggest a new feature.

Fork the repository and submit a pull request with your improvements.

Join our community channels (coming soon) to participate in the discussion.

📜 License
The NHBCoin L1 Node is open-source software licensed under the MIT License.