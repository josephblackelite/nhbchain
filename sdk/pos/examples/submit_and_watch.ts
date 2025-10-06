import { credentials } from "@grpc/grpc-js";
import { createPrivateKey, randomBytes, sign as edSign } from "crypto";
import { promisify } from "util";
import { MsgAuthorizePayment, TxClient } from "../../../clients/ts/pos/tx";
import {
  FinalityStatus,
  RealtimeClient,
  SubscribeFinalityResponse,
} from "../../../clients/ts/pos/realtime";

const TX_ENDPOINT = process.env.POS_TX_GRPC ?? "localhost:9090";
const REALTIME_ENDPOINT = process.env.POS_REALTIME_GRPC ?? "localhost:9090";
const PRIVATE_KEY_PEM = `-----BEGIN PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIEICMcFMi4mUlFP5w0JIweUlBl7U8tpyrkJc7m+aaxPIKn
-----END PRIVATE KEY-----`;

interface IntentEnvelope {
  intentRef: Buffer;
  amount: string;
  currency: string;
  expiry: number;
  merchant: string;
  device?: string;
  paymaster?: string;
}

const canonicalString = ({ intentRef, amount, currency, expiry, merchant, device, paymaster }: IntentEnvelope): string => {
  const parts = [
    `nhbpay://intent/${intentRef.toString("hex")}?amount=${amount}`,
    `currency=${currency}`,
    `expiry=${expiry}`,
    `merchant=${merchant}`,
  ];
  if (device) {
    parts.push(`device=${device}`);
  }
  if (paymaster) {
    parts.push(`paymaster=${paymaster}`);
  }
  return parts.join("&");
};

const buildUri = (envelope: IntentEnvelope, signature: Buffer): string => {
  const qp: string[] = [
    `amount=${encodeURIComponent(envelope.amount)}`,
    `currency=${encodeURIComponent(envelope.currency)}`,
    `expiry=${envelope.expiry}`,
    `merchant=${encodeURIComponent(envelope.merchant)}`,
  ];
  if (envelope.paymaster) {
    qp.push(`paymaster=${encodeURIComponent(envelope.paymaster)}`);
  }
  if (envelope.device) {
    qp.push(`device=${encodeURIComponent(envelope.device)}`);
  }
  if (signature.length > 0) {
    qp.push(`sig=${signature.toString("hex")}`);
  }
  return `nhbpay://intent/${envelope.intentRef.toString("hex")}?${qp.join("&")}`;
};

const authorizePayment = async (envelope: IntentEnvelope, payer: string): Promise<string> => {
  const txClient = new TxClient(TX_ENDPOINT, credentials.createInsecure());
  const request: MsgAuthorizePayment = {
    payer,
    merchant: envelope.merchant,
    amount: envelope.amount,
    expiry: envelope.expiry,
    intentRef: envelope.intentRef,
  };
  const unary = promisify(txClient.authorizePayment.bind(txClient));
  try {
    const response = await unary(request);
    return response.authorizationId;
  } finally {
    txClient.close();
  }
};

const watchFinality = async (intentRef: Buffer): Promise<void> => {
  const realtime = new RealtimeClient(REALTIME_ENDPOINT, credentials.createInsecure());
  try {
    await new Promise<void>((resolve, reject) => {
      const hexRef = intentRef.toString("hex");
      const stream = realtime.subscribeFinality({ cursor: "" });

      const onData = (payload: SubscribeFinalityResponse): void => {
        const update = payload.update;
        if (!update) {
          return;
        }
        const updateRef = update.intentRef ? Buffer.from(update.intentRef).toString("hex") : "";
        console.log(
          `status=${FinalityStatus[update.status] ?? "UNSPECIFIED"} cursor=${update.cursor} intent=${updateRef} tx=${
            update.txHash ? Buffer.from(update.txHash).toString("hex") : ""
          }`
        );
        if (updateRef === hexRef && update.status === FinalityStatus.FINALITY_STATUS_FINALIZED) {
          stream.cancel();
          resolve();
        }
      };

      stream.on("data", onData);
      stream.on("error", (err) => {
        stream.cancel();
        reject(err);
      });
      stream.on("end", () => {
        resolve();
      });
    });
  } finally {
    realtime.close();
  }
};

(async () => {
  try {
    const intentRef = randomBytes(32);
    const envelope: IntentEnvelope = {
      intentRef,
      amount: "15.25",
      currency: "USD",
      expiry: Math.floor(Date.now() / 1000) + 15 * 60,
      merchant: "nhb1m0ckmerchantaddre55",
      device: "kiosk-7",
      paymaster: "nhb1sponsorship",
    };

    const canonical = canonicalString(envelope);
    const privateKey = createPrivateKey(PRIVATE_KEY_PEM);
    const signature = edSign(null, Buffer.from(canonical, "utf8"), privateKey);
    const uri = buildUri(envelope, signature);

    console.log(`Generated NHB Pay URI: ${uri}`);

    const authorizationId = await authorizePayment(envelope, "nhb1samplepayer");
    console.log(`POS authorization id: ${authorizationId}`);

    console.log("Watching finality updates...");
    await watchFinality(intentRef);
    console.log("Intent finalized.");
  } catch (err) {
    console.error("POS submit and watch example failed", err);
    process.exitCode = 1;
  }
})();
