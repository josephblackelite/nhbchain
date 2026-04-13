import { credentials } from "@grpc/grpc-js";
import { createHash, randomBytes } from "crypto";

import { ConsensusServiceClient } from "../../../clients/ts/consensus/v1/consensus";
import { TxEnvelope, TxSignature } from "../../../clients/ts/consensus/v1/tx";
import { MsgSupply } from "../../../clients/ts/lending/v1/tx";
import { Any } from "../../../clients/ts/google/protobuf/any";

async function main() {
  const client = new ConsensusServiceClient(
    "localhost:9090",
    credentials.createInsecure()
  );

  const supply: MsgSupply = {
    supplier: "nhb1supplieraddress",
    poolId: "usd-pool-1",
    amount: "5000000",
  };

  const payload: Any = {
    typeUrl: "type.googleapis.com/lending.v1.MsgSupply",
    value: MsgSupply.encode(supply).finish(),
  };

  const envelope: TxEnvelope = {
    payload,
    nonce: 7,
    chainId: "localnet",
    fee: { amount: "1000", denom: "unhb", payer: supply.supplier },
    memo: "sdk ts supply",
  };

  const digest = createHash("sha256")
    .update(TxEnvelope.encode(envelope).finish())
    .digest();

  // Replace the placeholder signature with a wallet specific signing routine.
  const signature: TxSignature = {
    publicKey: Buffer.alloc(65),
    signature: randomBytes(65),
  };

  await new Promise<void>((resolve, reject) => {
    client.submitTxEnvelope({ tx: { envelope, signature } }, (err) => {
      if (err) {
        reject(err);
        return;
      }
      resolve();
    });
  }).catch((err) => {
    console.error("submit envelope failed (expected without signer)", err);
  });

  console.log(`built supply tx digest ${digest.toString("hex")}`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
