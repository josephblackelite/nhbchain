import { createServer } from 'http';
import { AddressInfo } from 'net';
import { once } from 'node:events';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'crypto';

import IdentityGatewayClient from '../src/identityGateway';

function makeUrl(server: ReturnType<typeof createServer>): string {
  const address = server.address() as AddressInfo;
  return `http://127.0.0.1:${address.port}`;
}

test('registerEmail signs and forwards the request', async (t) => {
  const requests: { method: string; path: string; headers: Record<string, string | string[]>; body: string }[] = [];
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (chunk) => chunks.push(chunk as Buffer));
    req.on('end', () => {
      const body = Buffer.concat(chunks).toString('utf8');
      requests.push({ method: req.method ?? '', path: req.url ?? '', headers: req.headers as Record<string, string | string[]>, body });
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ status: 'pending', expiresIn: 600 }));
    });
  });
  server.listen(0);
  await once(server, 'listening');
  t.after(() => server.close());

  const fixed = 1_700_000_000_000;
  const client = new IdentityGatewayClient({
    baseUrl: makeUrl(server),
    apiKey: 'demo',
    apiSecret: 'secret',
    fetchImpl: fetch,
    clock: () => fixed
  });
  const response = await client.registerEmail('User@example.com', 'alias', { idempotencyKey: 'test-key' });
  assert.deepEqual(response, { status: 'pending', expiresIn: 600 });
  assert.equal(requests.length, 1);
  const request = requests[0];
  assert.equal(request.method, 'POST');
  assert.equal(request.path, '/identity/email/register');
  assert.equal(request.headers['x-api-key'], 'demo');
  assert.equal(request.headers['idempotency-key'], 'test-key');
  const expectedPayload = JSON.stringify({ email: 'User@example.com', aliasHint: 'alias' });
  assert.equal(request.body, expectedPayload);
  const timestamp = Math.floor(fixed / 1000).toString();
  const bodyHash = crypto.createHash('sha256').update(expectedPayload).digest('hex');
  const expectedSignature = crypto
    .createHmac('sha256', 'secret')
    .update(`POST\n/identity/email/register\n${bodyHash}\n${timestamp}`)
    .digest('hex');
  assert.equal(request.headers['x-api-signature'], expectedSignature);
  assert.equal(request.headers['x-api-timestamp'], timestamp);
});

test('verifyEmail surfaces upstream errors', async (t) => {
  const server = createServer((_req, res) => {
    res.writeHead(401, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ error: { code: 'IDN-401' } }));
  });
  server.listen(0);
  await once(server, 'listening');
  t.after(() => server.close());

  const client = new IdentityGatewayClient({
    baseUrl: makeUrl(server),
    apiKey: 'demo',
    apiSecret: 'secret',
    fetchImpl: fetch
  });
  await assert.rejects(() => client.verifyEmail('user@example.com', '123456'), /identity gateway 401/);
});
