import { createServer } from 'http';
import { once } from 'events';
import { AddressInfo } from 'net';
import assert from 'node:assert/strict';
import { test } from 'node:test';

import EscrowDisputeClient from './dispute';

function makeUrl(server: ReturnType<typeof createServer>): string {
  const address = server.address() as AddressInfo;
  return `http://127.0.0.1:${address.port}`;
}

test('dispute helper resolves payer and forwards reason', async (t) => {
  const requests: Array<{ method: string; params: unknown[] }> = [];
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (chunk) => chunks.push(chunk as Buffer));
    req.on('end', () => {
      const payload = JSON.parse(Buffer.concat(chunks).toString('utf8')) as {
        method: string;
        params: unknown[];
      };
      requests.push({ method: payload.method, params: payload.params });
      if (payload.method === 'escrow_get') {
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(
          JSON.stringify({
            result: {
              id: 'ESC123',
              payer: 'nhb1payer0000000000000000000000000000000000',
              payee: 'nhb1payee000000000000000000000000000000000',
              status: 'EscrowFunded',
            },
          }),
        );
      } else if (payload.method === 'escrow_dispute') {
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ result: 'ok' }));
      } else {
        res.writeHead(400, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ error: { code: -32601, message: 'method not found' } }));
      }
    });
  });
  server.listen(0);
  await once(server, 'listening');
  t.after(() => server.close());

  const client = new EscrowDisputeClient({ baseUrl: makeUrl(server), authToken: 'secret' });
  const response = await client.dispute('ESC123', 'suspected fraud');
  assert.equal(response, 'ok');

  assert.equal(requests.length, 2);
  assert.equal(requests[0].method, 'escrow_get');
  assert.equal(requests[1].method, 'escrow_dispute');
  const disputeParams = requests[1].params[0] as Record<string, string>;
  assert.equal(disputeParams.caller, 'nhb1payer0000000000000000000000000000000000');
  assert.equal(disputeParams.reason, 'suspected fraud');
});

test('dispute helper omits empty reasons', async (t) => {
  const requests: Array<{ method: string; params: unknown[] }> = [];
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (chunk) => chunks.push(chunk as Buffer));
    req.on('end', () => {
      const payload = JSON.parse(Buffer.concat(chunks).toString('utf8')) as {
        method: string;
        params: unknown[];
      };
      requests.push({ method: payload.method, params: payload.params });
      if (payload.method === 'escrow_get') {
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(
          JSON.stringify({
            result: {
              id: 'ESC999',
              payer: 'nhb1payer0000000000000000000000000000000000',
              payee: 'nhb1payee000000000000000000000000000000000',
              status: 'EscrowFunded',
            },
          }),
        );
      } else if (payload.method === 'escrow_dispute') {
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ result: 'ok' }));
      }
    });
  });
  server.listen(0);
  await once(server, 'listening');
  t.after(() => server.close());

  const client = new EscrowDisputeClient({ baseUrl: makeUrl(server), authToken: 'secret' });
  await client.dispute('ESC999', '   ');
  const disputeParams = requests[1].params[0] as Record<string, string>;
  assert.equal('reason' in disputeParams, false);
});
