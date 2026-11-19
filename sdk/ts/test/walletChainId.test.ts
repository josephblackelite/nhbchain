import { test } from 'node:test';
import assert from 'node:assert/strict';

import WalletClient, { CHAIN_ID_REUSE_MS } from '../src/wallet';

// The chain id a transfer carries (ledger PL-R5T-29).
//
// Until the chain's activation height a node accepts the shared constant; from it, only the network's own id. The
// client reads the id the node reports (nhb_getNetworkStats, field txChainId), keeps it for a short time, and signs
// nothing when the node cannot say. There is no default id in the client.

// Larger than 2^53, so any float round trip would show.
const NETWORK_CHAIN_ID = '18346390202490284624';
const KEY = '0x4f3edf983ac636a65a842ce7c78d9aa706d3b113bce9c46f30d7c3c6d6c8f146';
const TO = 'nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgp6q0uya';

type Call = { method: string; auth?: string };
type StatsAnswer = { status?: number; body: unknown };

function fakeNode(stats: () => StatsAnswer) {
  const calls: Call[] = [];
  const fetchImpl: typeof fetch = async (_url, init) => {
    const method = JSON.parse(String(init?.body)).method as string;
    const headers = (init?.headers ?? {}) as Record<string, string>;
    calls.push({ method, auth: headers['authorization'] });
    let status = 200;
    let body: unknown;
    if (method === 'nhb_getNetworkStats') {
      const answer = stats();
      status = answer.status ?? 200;
      body = answer.body;
    } else if (method === 'nhb_getBalance') {
      body = { result: { nonce: 9 } };
    } else {
      body = { result: 'Transaction received by node.' };
    }
    return new Response(typeof body === 'string' ? body : JSON.stringify(body), {
      status,
      headers: { 'content-type': 'application/json' },
    });
  };
  return { calls, fetchImpl };
}

function newClient(node: ReturnType<typeof fakeNode>, extra: Record<string, unknown> = {}) {
  return new WalletClient({ baseUrl: 'http://node', authToken: 't', fetchImpl: node.fetchImpl, ...extra });
}

test('a transfer carries the id the node reports: the legacy constant before the activation, the network id after', async () => {
  for (const id of ['5130306', NETWORK_CHAIN_ID]) {
    const node = fakeNode(() => ({ body: { result: { txChainId: id } } }));
    const { transaction } = await newClient(node).sendTransfer({ recipient: TO, privateKey: KEY, amount: 1n });
    assert.equal(transaction.chainId, id);
  }
});

test('the id is part of what is signed: the same transfer on two ids has two signatures', async () => {
  const sigs = new Set<string>();
  for (const id of ['5130306', NETWORK_CHAIN_ID]) {
    const node = fakeNode(() => ({ body: { result: { txChainId: id } } }));
    const { transaction } = await newClient(node).sendTransfer({ recipient: TO, privateKey: KEY, amount: 1n });
    sigs.add(`${transaction.r}:${transaction.s}`);
  }
  assert.equal(sigs.size, 2);
});

test('nothing is signed or sent when the node cannot give a usable id', async () => {
  const bad: Record<string, () => StatsAnswer> = {
    'an HTTP error': () => ({ status: 502, body: { result: { txChainId: '7' } } }),
    'a JSON-RPC error': () => ({ body: { error: { code: -32601, message: 'method not found' } } }),
    'a node that does not report it': () => ({ body: { result: { activeValidators: 2 } } }),
    'a null result': () => ({ body: { result: null } }),
    'an empty string': () => ({ body: { result: { txChainId: '' } } }),
    zero: () => ({ body: { result: { txChainId: '0' } } }),
    'zero as a number': () => ({ body: { result: { txChainId: 0 } } }),
    negative: () => ({ body: { result: { txChainId: '-5' } } }),
    hex: () => ({ body: { result: { txChainId: '0x4e4842' } } }),
    text: () => ({ body: { result: { txChainId: 'NHB' } } }),
    fraction: () => ({ body: { result: { txChainId: '12.5' } } }),
    'a plus sign': () => ({ body: { result: { txChainId: '+7' } } }),
    'above a uint64': () => ({ body: { result: { txChainId: '18446744073709551616' } } }),
    'a number past 2^53': () => ({ body: { result: { txChainId: 18346390202490284624 } } }),
    'a boolean': () => ({ body: { result: { txChainId: true } } }),
    'not json': () => ({ body: 'not json' }),
  };
  for (const [name, stats] of Object.entries(bad)) {
    const node = fakeNode(stats);
    await assert.rejects(
      () => newClient(node).sendTransfer({ recipient: TO, privateKey: KEY, amount: 1n }),
      /transaction chain id/i,
      name,
    );
    assert.ok(!node.calls.some((c) => c.method === 'nhb_sendTransaction'), `${name}: a transaction was sent`);
    assert.ok(!node.calls.some((c) => c.method === 'nhb_getBalance'), `${name}: the node was asked for a nonce first`);
  }
});

test('an unreachable node gives no signed transfer', async () => {
  const fetchImpl: typeof fetch = async () => {
    throw new TypeError('fetch failed');
  };
  const client = new WalletClient({ baseUrl: 'http://node', authToken: 't', fetchImpl });
  await assert.rejects(() => client.sendTransfer({ recipient: TO, privateKey: KEY, amount: 1n }), /transaction chain id/i);
});

test('an explicit chain id is used and the node is not asked for one', async () => {
  const node = fakeNode(() => ({ body: { result: { txChainId: NETWORK_CHAIN_ID } } }));
  const client = newClient(node, { chainId: 777n });
  const { transaction } = await client.sendTransfer({ recipient: TO, privateKey: KEY, amount: 1n });
  assert.equal(transaction.chainId, '777');
  assert.ok(!node.calls.some((c) => c.method === 'nhb_getNetworkStats'));
  assert.equal(await client.txChainId(), 777n);
});

test('the node is asked once for several transfers and again after the expiry', async () => {
  let current = '5130306';
  let clock = 1_000_000;
  const node = fakeNode(() => ({ body: { result: { txChainId: current } } }));
  const client = newClient(node, { chainIdTtlMs: 15_000, now: () => clock });
  const stats = () => node.calls.filter((c) => c.method === 'nhb_getNetworkStats').length;
  for (let i = 0; i < 4; i++) {
    await client.sendTransfer({ recipient: TO, privateKey: KEY, amount: 1n });
  }
  assert.equal(stats(), 1);
  // The activation height passes.
  current = NETWORK_CHAIN_ID;
  clock += 14_999;
  assert.equal((await client.sendTransfer({ recipient: TO, privateKey: KEY, amount: 1n })).transaction.chainId, '5130306');
  assert.equal(stats(), 1);
  clock += 1; // exactly the expiry
  assert.equal((await client.sendTransfer({ recipient: TO, privateKey: KEY, amount: 1n })).transaction.chainId, NETWORK_CHAIN_ID);
  assert.equal(stats(), 2);
});

test('concurrent transfers share one read and a failure is not kept', async () => {
  let ok = false;
  let calls = 0;
  const node = fakeNode(() => {
    calls++;
    return ok ? { body: { result: { txChainId: '42' } } } : { status: 500, body: {} };
  });
  const client = newClient(node);
  const failures = await Promise.allSettled([client.txChainId(), client.txChainId(), client.txChainId()]);
  assert.ok(failures.every((r) => r.status === 'rejected'));
  assert.equal(calls, 1, 'three concurrent callers share one node call');
  ok = true;
  assert.equal(await client.txChainId(), 42n);
  assert.equal(await client.txChainId(), 42n);
  assert.equal(calls, 2);
});

test('an expiry that is not a positive number is refused when the client is built', () => {
  const node = fakeNode(() => ({ body: { result: { txChainId: '1' } } }));
  for (const bad of [0, -1, Number.NaN]) {
    assert.throws(() => newClient(node, { chainIdTtlMs: bad }), /expiry/i);
  }
});

test('the client carries no chain id constant', async () => {
  const { readFile } = await import('node:fs/promises');
  const src = await readFile(new URL('../src/wallet.ts', import.meta.url), 'utf8');
  assert.doesNotMatch(src, /0x4e4842/i);
  assert.doesNotMatch(src, /DEFAULT_CHAIN_ID/);
});

test('the default reuse time is short: well under a minute', () => {
  assert.ok(CHAIN_ID_REUSE_MS > 0);
  assert.ok(CHAIN_ID_REUSE_MS <= 60_000);
});
