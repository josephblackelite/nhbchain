import { credentials } from "@grpc/grpc-js";
import {
  ConsensusServiceClient,
  SubmitTransactionRequest,
} from "../../clients/ts/consensus/v1/consensus";

async function main() {
  const client = new ConsensusServiceClient(
    "localhost:50051",
    credentials.createInsecure()
  );

  const height = await new Promise<number>((resolve, reject) => {
    client.getHeight({}, (err, resp) => {
      if (err) {
        reject(err);
        return;
      }
      resolve(resp?.height ?? 0);
    });
  });
  console.log(`current height: ${height}`);

  const txRequest: SubmitTransactionRequest = {
    transaction: { nonce: 1 },
  };

  await new Promise<void>((resolve, reject) => {
    client.submitTransaction(txRequest, (err) => {
      if (err) {
        reject(err);
        return;
      }
      resolve();
    });
  }).catch((err) => {
    console.error("submit transaction failed (expected without server)", err);
  });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
