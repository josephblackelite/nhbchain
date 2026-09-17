import http from 'http';
import path from 'path';
import { fileURLToPath } from 'url';
import dotenv from 'dotenv';
import { hmacSign, walletSig } from '@nhb/examples-lib-sdk';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

dotenv.config({ path: path.resolve(__dirname, '../../.env') });

const port = Number(process.env.NETWORK_MONITOR_PORT || 4301);

function json(res, body, status = 200) {
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(body, null, 2));
}

async function collectBody(req) {
  const chunks = [];
  for await (const chunk of req) {
    chunks.push(Buffer.from(chunk));
  }
  if (chunks.length === 0) return '';
  const buffer = Buffer.concat(chunks);
  try {
    return JSON.parse(buffer.toString('utf8'));
  } catch (err) {
    return buffer.toString('utf8');
  }
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url ?? '/', 'http://localhost');

  if (url.pathname === '/health') {
    json(res, { ok: true, service: 'network-monitor', port });
    return;
  }

  if (url.pathname === '/simulate-sign' && req.method === 'POST') {
    const payload = await collectBody(req);
    const timestamp = new Date().toISOString();
    const body = payload || { sample: 'body' };
    const secret = process.env.NHB_API_SECRET || 'demo-api-secret';
    const signature = hmacSign(body, secret, timestamp);

    let walletSignature = null;
    if (process.env.NHB_WALLET_PRIVATE_KEY) {
      try {
        walletSignature = await walletSig(JSON.stringify(body), process.env.NHB_WALLET_PRIVATE_KEY);
      } catch (error) {
        walletSignature = `Unable to sign with wallet: ${error.message}`;
      }
    }

    json(res, {
      received: body,
      timestamp,
      hmacSignature: signature,
      walletSignature,
    });
    return;
  }

  if (url.pathname === '/metrics' && req.method === 'GET') {
    json(res, {
      status: 'ok',
      rpcHttpUrl: process.env.NHB_RPC_URL,
      rpcWsUrl: process.env.NHB_WS_URL,
      apiUrl: process.env.NHB_API_URL,
      chainId: process.env.NHB_CHAIN_ID,
    });
    return;
  }

  res.writeHead(200, { 'Content-Type': 'text/plain' });
  res.end(
    [
      'NHB Network Monitor example server',
      '',
      `Listening on http://localhost:${port}`,
      'Available routes:',
      '  GET  /health          -> Workspace health probe',
      '  GET  /metrics         -> Returns configured gateway endpoints',
      '  POST /simulate-sign   -> Signs an arbitrary payload with HMAC and wallet helpers',
    ].join('\n'),
  );
});

server.listen(port, () => {
  console.log(`Network monitor listening on http://localhost:${port}`);
});
