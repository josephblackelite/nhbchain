# Developer Cookbooks

The following quickstarts demonstrate the new service topology using real code
snippets that compile as part of `make docs:verify`.

## First transaction

1. Export your consensus endpoint (`CONSENSUSD_GRPC_ADDR`) and funding key.
2. Use the SDK helpers to assemble a lending supply transaction.
3. Submit the signed envelope to the consensus service.

<!-- embed:examples/docs/go/first_transaction/main.go -->
```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"nhbchain/crypto"
	cons "nhbchain/sdk/consensus"
	"nhbchain/sdk/lending"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	endpoint := os.Getenv("CONSENSUSD_GRPC_ADDR")
	if endpoint == "" {
		endpoint = "localhost:9090"
	}

	client, err := cons.Dial(ctx, endpoint, cons.WithInsecure())
	if err != nil {
		log.Fatalf("dial consensus: %v", err)
	}
	defer client.Close()

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		log.Fatalf("generate key: %v", err)
	}
	sender := key.PubKey().Address().String()

	supplyMsg, err := lending.NewMsgSupply(sender, "usd-pool-1", "1000000")
	if err != nil {
		log.Fatalf("build supply msg: %v", err)
	}

	envelope, err := cons.NewTx(supplyMsg, 1, "localnet", "1000", "znhb", sender, "first transaction demo")
	if err != nil {
		log.Fatalf("build envelope: %v", err)
	}

	signed, err := cons.Sign(envelope, key)
	if err != nil {
		log.Fatalf("sign envelope: %v", err)
	}

	if err := client.SubmitEnvelope(ctx, signed); err != nil {
		log.Fatalf("submit envelope: %v", err)
	}
	fmt.Printf("broadcasted supply from %s to pool %s\n", sender, supplyMsg.GetPoolId())
}
```

<!-- embed:examples/docs/ts/first-transaction.ts -->
```ts
import path from 'node:path';
import process from 'node:process';
import { credentials, ClientUnaryCall, ServiceError, Metadata } from '@grpc/grpc-js';
import { loadPackageDefinition } from '@grpc/grpc-js';
import { loadSync } from '@grpc/proto-loader';

type LendingServiceClient = {
  SupplyAsset(
    request: { account: string; market?: { symbol: string }; amount: string },
    metadata: Metadata,
    callback: (err: ServiceError | null, response: { position?: unknown }) => void
  ): ClientUnaryCall;
};

type LendingServiceCtor = new (address: string, creds: ReturnType<typeof credentials.createInsecure>) => LendingServiceClient;

const protoRoot = path.resolve(__dirname, '../../../proto/lending/v1/lending.proto');
const definition = loadSync(protoRoot, {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true
});

const pkg = loadPackageDefinition(definition) as unknown as {
  lending: {
    v1: {
      LendingService: LendingServiceCtor;
    };
  };
};

const endpoint = process.env.LENDING_GRPC_ADDR ?? 'localhost:9444';
const client = new pkg.lending.v1.LendingService(endpoint, credentials.createInsecure());

client.SupplyAsset(
  {
    account: process.env.LENDING_ACCOUNT ?? 'nhb1exampleaddress',
    market: { symbol: 'usd-pool-1' },
    amount: '1000000'
  },
  new Metadata(),
  (err, resp) => {
    if (err) {
      console.error('supply asset failed', err);
      return;
    }
    console.log('submitted first transaction, position snapshot:', resp.position ?? {});
  }
);
```

## Query positions

1. Call the consensus query API for the lending module.
2. Parse the JSON payload into a friendly structure.
3. Render the position summary to standard out.

<!-- embed:examples/queries/lending_positions.go -->
```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"nhbchain/sdk/consensus"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	endpoint := os.Getenv("CONSENSUSD_GRPC_ADDR")
	if endpoint == "" {
		endpoint = "localhost:9090"
	}
	addr := os.Getenv("LENDING_ADDRESS")
	if addr == "" {
		log.Fatal("set LENDING_ADDRESS (bech32 or 0x hex) to query positions")
	}

	client, err := consensus.Dial(ctx, endpoint, consensus.WithInsecure())
	if err != nil {
		log.Fatalf("dial consensus service: %v", err)
	}
	defer client.Close()

	value, _, err := client.QueryState(ctx, "lending", fmt.Sprintf("positions/%s", addr))
	if err != nil {
		log.Fatalf("query positions: %v", err)
	}
	if len(value) == 0 {
		fmt.Println("no active positions for address")
		return
	}
	var decoded []map[string]any
	if err := json.Unmarshal(value, &decoded); err != nil {
		log.Fatalf("decode response: %v", err)
	}
	pretty, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		log.Fatalf("format response: %v", err)
	}
	fmt.Printf("Positions for %s:\n%s\n", addr, string(pretty))
}
```

<!-- embed:examples/docs/ts/query-positions.ts -->
```ts
import path from 'node:path';
import process from 'node:process';
import { credentials, ClientUnaryCall, ServiceError, Metadata } from '@grpc/grpc-js';
import { loadPackageDefinition } from '@grpc/grpc-js';
import { loadSync } from '@grpc/proto-loader';

type LendingServiceClient = {
  GetPosition(
    request: { account: string },
    metadata: Metadata,
    callback: (err: ServiceError | null, response: { position?: unknown }) => void
  ): ClientUnaryCall;
};

type LendingServiceCtor = new (address: string, creds: ReturnType<typeof credentials.createInsecure>) => LendingServiceClient;

const protoRoot = path.resolve(__dirname, '../../../proto/lending/v1/lending.proto');
const definition = loadSync(protoRoot, {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true
});

const pkg = loadPackageDefinition(definition) as unknown as {
  lending: {
    v1: {
      LendingService: LendingServiceCtor;
    };
  };
};

const endpoint = process.env.LENDING_GRPC_ADDR ?? 'localhost:9444';
const client = new pkg.lending.v1.LendingService(endpoint, credentials.createInsecure());

client.GetPosition(
  { account: process.env.LENDING_ACCOUNT ?? 'nhb1exampleaddress' },
  new Metadata(),
  (err, resp) => {
    if (err) {
      console.error('position query failed', err);
      return;
    }
    console.log('active positions', JSON.stringify(resp.position ?? {}, null, 2));
  }
);
```

## Publish a price oracle update

1. Establish a streaming session with the price oracle service.
2. Sign and send a price observation payload.
3. Await acknowledgement and log the resulting attestation identifier.

<!-- embed:examples/docs/go/price_oracle_publish/main.go -->
```go
package main

import (
        "context"
        "encoding/json"
        "fmt"
        "log"
        "os"
        "time"

        "google.golang.org/grpc"
        "google.golang.org/grpc/credentials/insecure"

        networkv1 "nhbchain/proto/network/v1"
)

func main() {
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()

        endpoint := os.Getenv("ORACLE_GRPC_ADDR")
        if endpoint == "" {
                endpoint = "localhost:9555"
        }

        conn, err := grpc.DialContext(ctx, endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
        if err != nil {
                log.Fatalf("dial price oracle: %v", err)
        }
        defer conn.Close()

        client := networkv1.NewNetworkServiceClient(conn)
        stream, err := client.Gossip(ctx)
        if err != nil {
                log.Fatalf("open gossip stream: %v", err)
        }
        defer stream.CloseSend()

        payload, err := json.Marshal(map[string]any{
                "symbol":     "NHB/USD",
                "price":      "1.0002",
                "timestamp":  time.Now().UTC().Format(time.RFC3339),
                "publisherId": "oracle-publisher-1",
        })
        if err != nil {
                log.Fatalf("marshal price payload: %v", err)
        }

        msg := &networkv1.GossipRequest{
                Envelope: &networkv1.NetworkEnvelope{
                        Event: &networkv1.NetworkEnvelope_Gossip{
                                Gossip: &networkv1.GossipMessage{
                                        Type:    7001,
                                        Payload: payload,
                                },
                        },
                },
        }
        if err := stream.Send(msg); err != nil {
                log.Fatalf("send price gossip: %v", err)
        }

        ack, err := stream.Recv()
        if err != nil {
                log.Fatalf("receive oracle ack: %v", err)
        }
        ackEnvelope := ack.GetEnvelope()
        if ackEnvelope == nil {
                fmt.Println("oracle acknowledged price update with empty envelope")
                return
        }
        if gossip := ackEnvelope.GetGossip(); gossip != nil {
                fmt.Printf("oracle acknowledged price update: %s\n", string(gossip.GetPayload()))
        } else {
                fmt.Println("oracle acknowledged price update with empty payload")
        }
}
```

<!-- embed:examples/docs/ts/price-oracle-publish.ts -->
```ts
import path from 'node:path';
import process from 'node:process';
import {
  credentials,
  ClientDuplexStream,
  ChannelCredentials,
  ServiceError
} from '@grpc/grpc-js';
import { loadPackageDefinition } from '@grpc/grpc-js';
import { loadSync } from '@grpc/proto-loader';

type GossipEnvelope = {
  envelope?: {
    gossip?: {
      type?: number;
      payload?: Buffer;
    };
  };
};

type NetworkServiceClient = {
  Gossip(): ClientDuplexStream<GossipEnvelope, GossipEnvelope>;
};

type NetworkServiceCtor = new (address: string, creds: ChannelCredentials) => NetworkServiceClient;

const protoRoot = path.resolve(__dirname, '../../../proto/network/v1/network.proto');
const definition = loadSync(protoRoot, {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true,
  bytes: Buffer
});

const pkg = loadPackageDefinition(definition) as unknown as {
  network: {
    v1: {
      NetworkService: NetworkServiceCtor;
    };
  };
};

const endpoint = process.env.ORACLE_GRPC_ADDR ?? 'localhost:9555';
const client = new pkg.network.v1.NetworkService(endpoint, credentials.createInsecure());
const stream = client.Gossip();

stream.on('data', (msg: GossipEnvelope) => {
  const payload = msg.envelope?.gossip?.payload;
  if (payload) {
    console.log('oracle ack payload', payload.toString('utf8'));
  } else {
    console.log('oracle ack with empty payload');
  }
  stream.end();
});

stream.on('error', (err: ServiceError) => {
  console.error('oracle gossip error', err);
});

const pricePayload = Buffer.from(
  JSON.stringify({
    symbol: 'NHB/USD',
    price: '1.0002',
    timestamp: new Date().toISOString(),
    publisherId: process.env.ORACLE_PUBLISHER_ID ?? 'oracle-publisher-1'
  }),
  'utf8'
);

stream.write({
  envelope: {
    gossip: {
      type: 7001,
      payload: pricePayload
    }
  }
});
```
