// Sample realtime subscriber demonstrating gRPC and WebSocket reconnect logic.
// Usage: POS_REALTIME_GRPC=host:port POS_REALTIME_WS=wss://... ts-node subscriber.ts
import { credentials } from "@grpc/grpc-js";
import WebSocket from "ws";
import {
  FinalityStatus,
  FinalityUpdate,
  RealtimeClient,
  SubscribeFinalityResponse,
} from "../../../clients/ts/pos/realtime";

const GRPC_ENDPOINT = process.env.POS_REALTIME_GRPC ?? "localhost:9090";
const WS_ENDPOINT = process.env.POS_REALTIME_WS ?? "ws://localhost:8545/ws/pos/finality";
const SAMPLE_WINDOW_MS = Number(process.env.POS_SAMPLE_WINDOW_MS ?? "10000");

const toHex = (bytes?: Uint8Array): string => {
  if (!bytes || bytes.length === 0) {
    return "";
  }
  return `0x${Buffer.from(bytes).toString("hex")}`;
};

const describeUpdate = (update: FinalityUpdate): string => {
  const parts = [
    `cursor=${update.cursor}`,
    `status=${FinalityStatus[update.status] ?? "UNKNOWN"}`,
    `intent=${toHex(update.intentRef)}`,
    `tx=${toHex(update.txHash)}`,
  ];
  if (update.height) {
    parts.push(`height=${update.height}`);
  }
  if (update.blockHash && update.blockHash.length > 0) {
    parts.push(`block=${toHex(update.blockHash)}`);
  }
  parts.push(`ts=${update.timestamp}`);
  return parts.join(" ");
};

const runGrpcSample = (cursor: string, windowMs: number): Promise<string> => {
  const client = new RealtimeClient(GRPC_ENDPOINT, credentials.createInsecure());
  return new Promise((resolve, reject) => {
    let latest = cursor;
    const stream = client.subscribeFinality({ cursor });
    const timeout = setTimeout(() => {
      stream.cancel();
    }, windowMs);

    stream.on("data", (response: SubscribeFinalityResponse) => {
      const update = response.update;
      if (!update) {
        return;
      }
      latest = update.cursor || latest;
      console.log(`[gRPC] ${describeUpdate(update)}`);
    });

    stream.on("error", (err) => {
      clearTimeout(timeout);
      reject(err);
    });

    stream.on("end", () => {
      clearTimeout(timeout);
      resolve(latest);
    });
  });
};

const runWebsocketSample = (cursor: string, windowMs: number): Promise<string> => {
  const url = cursor ? `${WS_ENDPOINT}?cursor=${encodeURIComponent(cursor)}` : WS_ENDPOINT;
  return new Promise((resolve, reject) => {
    let latest = cursor;
    const ws = new WebSocket(url);
    const timeout = setTimeout(() => {
      ws.close();
    }, windowMs);

    ws.on("message", (raw) => {
      try {
        const payload = JSON.parse(raw.toString());
        if (typeof payload?.cursor === "string") {
          latest = payload.cursor;
        }
        console.log(
          `[WS] cursor=${payload.cursor ?? latest} status=${payload.status} intent=${payload.intentRef} tx=${payload.txHash} height=${payload.height ?? 0} ts=${payload.ts}`,
        );
      } catch (err) {
        console.warn("invalid websocket payload", err);
      }
    });

    ws.on("close", () => {
      clearTimeout(timeout);
      resolve(latest);
    });

    ws.on("error", (err) => {
      clearTimeout(timeout);
      reject(err);
    });
  });
};

(async () => {
  try {
    console.log(`Connecting to POS realtime stream via gRPC at ${GRPC_ENDPOINT}`);
    const afterGrpc = await runGrpcSample(process.env.POS_CURSOR ?? "", SAMPLE_WINDOW_MS);
    console.log(`Reconnecting via WebSocket with cursor ${afterGrpc}`);
    await runWebsocketSample(afterGrpc, SAMPLE_WINDOW_MS);
    console.log("Sample complete. Re-run to continue tailing updates.");
  } catch (err) {
    console.error("POS realtime subscriber error", err);
    process.exitCode = 1;
  }
})();
