import { credentials } from "@grpc/grpc-js";
import { createHash, randomBytes } from "crypto";

import { ConsensusServiceClient } from "../../../clients/ts/consensus/v1/consensus";
import { TxEnvelope, TxSignature } from "../../../clients/ts/consensus/v1/tx";
import { MsgSupply } from "../../../clients/ts/lending/v1/tx";
import { Any } from "../../../clients/ts/google/protobuf/any";

// The chain id the envelope carries is the one the node reports as txChainId in the public nhb_getNetworkStats
// call (NHB_CHAIN_ID, when set, is an explicit choice). The id a node accepts changes once, at the chain's
// activation height, so it is asked for. If the node cannot say, nothing is built.
async function readChainId(): Promise<string> {
  const explicit = (process.env.NHB_CHAIN_ID ?? "").trim();
  if (explicit) return explicit;
  const rpcUrl = process.env.NHB_RPC_URL ?? "http://localhost:8545";
  const response = await fetch(rpcUrl, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "nhb_getNetworkStats", params: [] }),
  });
  if (!response.ok) throw new Error(`nhb_getNetworkStats failed with status ${response.status}`);
  const body = (await response.json()) as { result?: { txChainId?: unknown }; error?: { message?: string } };
  const id = body.result?.txChainId;
  if (typeof id !== "string" || !/^[0-9]+$/.test(id) || BigInt(id) <= 0n) {
    throw new Error(`the node did not report a usable txChainId${body.error?.message ? `: ${body.error.message}` : ""}`);
  }
  return id;
}

async function main() {
  const chainId = await readChainId();
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
    chainId,
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
