import http from 'http';
import path from 'path';
import { fileURLToPath } from 'url';
import dotenv from 'dotenv';
import {
  rpcClient,
  createIdempotencyKey,
  idempotencyHeader,
  bech32Helpers,
} from '@nhb/examples-lib-sdk';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

dotenv.config({ path: path.resolve(__dirname, '../../.env') });

const port = Number(process.env.STATUS_DASHBOARD_PORT || 4300);

const client = rpcClient({
  baseUrl: process.env.NHB_RPC_URL,
  apiKey: process.env.NHB_API_KEY,
  apiSecret: process.env.NHB_API_SECRET,
  chainId: process.env.NHB_CHAIN_ID,
});

function json(res, body, status = 200) {
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(body, null, 2));
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url ?? '/', 'http://localhost');

  if (url.pathname === '/health') {
    json(res, { ok: true, service: 'status-dashboard', port });
    return;
  }

  if (url.pathname === '/rpc-status' && req.method === 'GET') {
    try {
      const status = await client.request('status', []);
      json(res, { ok: true, status });
    } catch (error) {
      json(
        res,
        {
          ok: false,
          message: 'Unable to query RPC status. Confirm NHB_RPC_URL is reachable.',
          error: error.message,
        },
        502,
      );
    }
    return;
  }

  if (url.pathname === '/demo-signature' && req.method === 'GET') {
    const key = createIdempotencyKey();
    json(res, {
      idempotencyKey: key,
      headers: idempotencyHeader(key),
      sampleAddress: process.env.NHB_WALLET_ADDRESS,
      bech32Decoded: process.env.NHB_WALLET_ADDRESS
        ? bech32Helpers.decode(process.env.NHB_WALLET_ADDRESS)
        : null,
    });
    return;
  }

  res.writeHead(200, { 'Content-Type': 'text/plain' });
  res.end(
    [
      'NHB Status Dashboard example server',
      '',
      `Listening on http://localhost:${port}`,
      'Available routes:',
      '  GET /health           -> Workspace health probe',
      '  GET /rpc-status       -> Proxy status RPC call',
      '  GET /demo-signature   -> Demonstrates idempotency helpers',
    ].join('\n'),
  );
});

server.listen(port, () => {
  console.log(`Status dashboard listening on http://localhost:${port}`);
});
