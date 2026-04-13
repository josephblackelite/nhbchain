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
