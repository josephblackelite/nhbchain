import { createServer } from 'http';
import { AddressInfo } from 'net';
import { once } from 'node:events';
import { test } from 'node:test';
import assert from 'node:assert/strict';

import WalletClient, { TRANSFER_TYPE_ZNHB } from '../src/wallet';

function makeUrl(server: ReturnType<typeof createServer>): string {
  const address = server.address() as AddressInfo;
  return `http://127.0.0.1:${address.port}`;
}

test('sendTransfer builds and submits a ZNHB transfer', async (t) => {
  const requests: { method: string; body: string; headers: Record<string, string | string[]> }[] = [];
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (chunk) => chunks.push(chunk as Buffer));
    req.on('end', () => {
      const body = Buffer.concat(chunks).toString('utf8');
      const parsed = JSON.parse(body);
      requests.push({ method: parsed.method, body, headers: req.headers });
      if (parsed.method === 'nhb_getBalance') {
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ result: { nonce: 3 } }));
      } else if (parsed.method === 'nhb_sendTransaction') {
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ result: 'Transaction received by node.' }));
      } else {
        res.writeHead(400, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ error: { code: -32601, message: 'method not found' } }));
      }
    });
  });
  server.listen(0);
  await once(server, 'listening');
  t.after(() => server.close());

  const client = new WalletClient({ baseUrl: makeUrl(server), authToken: 'secret' });
  const recipient = 'nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgp6q0uya';
  const privateKey = '0x4f3edf983ac636a65a842ce7c78d9aa706d3b113bce9c46f30d7c3c6d6c8f146';
  const amount = 1_000_000_000_000_000_000n;

  const result = await client.sendTransfer({ recipient, privateKey, amount, asset: 'ZNHB' });
  assert.equal(result.response, 'Transaction received by node.');
  assert.equal(result.transaction.type, TRANSFER_TYPE_ZNHB);
  assert.equal(result.transaction.value, amount.toString());
  assert.equal(result.transaction.nonce, '3');
  assert.equal(result.transaction.gasLimit, '25000');
  assert.equal(result.transaction.gasPrice, '1');
  assert.ok(result.transaction.r.length > 0);
  assert.ok(result.transaction.s.length > 0);
  assert.ok(result.transaction.v === '27' || result.transaction.v === '28');

  assert.equal(requests.length, 2);
  const sendRequest = requests.find((req) => req.method === 'nhb_sendTransaction');
  assert.ok(sendRequest, 'expected send transaction request');
  assert.equal(sendRequest!.headers['authorization'], 'Bearer secret');
  assert.match(sendRequest!.body, /"value":1000000000000000000/);
  assert.match(sendRequest!.body, /"type":16/);
});

test('client validates inputs', async () => {
  const okFetch: typeof fetch = async () =>
    new Response(JSON.stringify({ result: { nonce: 0 } }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  const client = new WalletClient({ baseUrl: 'http://localhost', authToken: 'secret', fetchImpl: okFetch });
  const privateKey = '0x4f3edf983ac636a65a842ce7c78d9aa706d3b113bce9c46f30d7c3c6d6c8f146';
  const recipient = 'nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgp6q0uya';
  await assert.rejects(
    () => client.sendTransfer({ recipient: 'invalid', privateKey, amount: 1n }),
    /Invalid NHB address/,
  );
  await assert.rejects(
    () => client.sendTransfer({ recipient, privateKey, amount: 0n }),
    /must be positive/,
  );
  const noAuthClient = new WalletClient({ baseUrl: 'http://localhost', fetchImpl: okFetch });
  await assert.rejects(
    () => noAuthClient.sendTransfer({ recipient, privateKey, amount: 1n }),
    /requires an authorization token/,
  );
});
