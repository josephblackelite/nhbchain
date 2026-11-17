import { createServer } from 'http';
import { once } from 'events';
import { AddressInfo } from 'net';
import assert from 'node:assert/strict';
import { test } from 'node:test';

import EscrowDisputeClient, { ESCROW_DISPUTE_RETIRED } from './dispute';

function makeUrl(server: ReturnType<typeof createServer>): string {
  const address = server.address() as AddressInfo;
  return `http://127.0.0.1:${address.port}`;
}

// The node answers escrow_dispute with HTTP 410, so the helper must not call it. The server here counts what
// reaches it (and would answer 200 to anything, as the old mock did, which is how the old test passed while the
// real node refused).
test('dispute helper fails at once, says it is retired and sends nothing to the node', async (t) => {
  let requests = 0;
  const server = createServer((_req, res) => {
    requests += 1;
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ result: 'ok' }));
  });
  server.listen(0);
  await once(server, 'listening');
  t.after(() => server.close());

  const client = new EscrowDisputeClient({ baseUrl: makeUrl(server), authToken: 'secret' });
  await assert.rejects(client.dispute('ESC123', 'suspected fraud'), (err: Error) => {
    assert.equal(err.message, ESCROW_DISPUTE_RETIRED);
    assert.match(err.message, /TxTypeDisputeEscrow/);
    assert.match(err.message, /nhb_sendTransaction/);
    return true;
  });
  assert.equal(requests, 0);
});

test('dispute helper still checks its inputs', async () => {
  const client = new EscrowDisputeClient({ baseUrl: 'http://127.0.0.1:1', authToken: 'secret', fetchImpl: fetch });
  await assert.rejects(client.dispute('   '), /escrowId is required/);
  assert.throws(() => new EscrowDisputeClient({ baseUrl: '', authToken: 'secret', fetchImpl: fetch }), /baseUrl is required/);
  assert.throws(() => new EscrowDisputeClient({ baseUrl: 'http://x', authToken: ' ', fetchImpl: fetch }), /authToken is required/);
});
