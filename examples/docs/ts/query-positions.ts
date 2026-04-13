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
