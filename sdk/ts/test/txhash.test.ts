import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { keccak_256 } from '@noble/hashes/sha3';
import { getPublicKey, signAsync, Signature } from '@noble/secp256k1';

import WalletClient, { TRANSFER_TYPE_NHB } from '../src/wallet';
import { hashTransactionV3, TxHashFields } from '../src/txhash.js';

// The vectors are written by the node's Go code from Transaction.Hash and
// Transaction.Sign (core/types/transaction_vectors_test.go). These tests check that
// the SDK produces the node's digest, the node's (deterministic) signature, and that
// a signature over the digest is attributed to the sender (PL-DC-11).

interface Vector {
  name: string;
  chainId: string;
  type: number;
  nonce: string;
  maxBlockHeight: string;
  intentExpiry: string;
  intentRef: string;
  to: string;
  value: string;
  data: string;
  gasLimit: string;
  gasPrice: string;
  paymaster: string;
  merchantAddress: string;
  deviceId: string;
  refundOf: string;
  hash: string;
  r: string;
  s: string;
  v: string;
}

const file = JSON.parse(
  readFileSync(new URL('./vectors/tx-hash-vectors.json', import.meta.url), 'utf8'),
) as { privateKey: string; address: string; vectors: Vector[] };

function fromHex(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i += 1) {
    out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

function toHex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

function fieldsOf(v: Vector): TxHashFields {
  return {
    chainId: BigInt(v.chainId),
    type: v.type,
    nonce: BigInt(v.nonce),
    maxBlockHeight: BigInt(v.maxBlockHeight),
    intentExpiry: BigInt(v.intentExpiry),
    intentRef: fromHex(v.intentRef),
    to: fromHex(v.to),
    value: BigInt(v.value),
    data: fromHex(v.data),
    gasLimit: BigInt(v.gasLimit),
    gasPrice: BigInt(v.gasPrice),
    paymaster: fromHex(v.paymaster),
    merchantAddress: v.merchantAddress,
    deviceId: v.deviceId,
    refundOf: v.refundOf,
  };
}

function addressOf(publicKeyUncompressed: Uint8Array): string {
  return toHex(keccak_256(publicKeyUncompressed.slice(1)).slice(-20));
}

test('the vector file is the one the Go test writes and is not empty', () => {
  assert.ok(file.vectors.length >= 8, 'expected the full vector set');
  assert.equal(file.address.length, 40);
});

for (const vector of file.vectors) {
  test(`digest matches the node: ${vector.name}`, () => {
    assert.equal(toHex(hashTransactionV3(fieldsOf(vector))), vector.hash);
  });

  test(`signature matches the node and is attributed to the sender: ${vector.name}`, async () => {
    const digest = hashTransactionV3(fieldsOf(vector));
    const key = fromHex(file.privateKey);
    const signature = await signAsync(digest, key);
    // ECDSA with RFC 6979 is deterministic, so the same key over the same digest
    // gives the node's own signature.
    assert.equal(signature.r.toString(), vector.r);
    assert.equal(signature.s.toString(), vector.s);
    assert.equal(String((signature.recovery ?? 0) + 27), vector.v);

    // Independently of the node's numbers: recover the signer from the signature.
    const recovered = new Signature(BigInt(vector.r), BigInt(vector.s), Number(BigInt(vector.v) - 27n))
      .recoverPublicKey(digest)
      .toRawBytes(false);
    assert.equal(addressOf(recovered), file.address);
  });
}

test('the digest changes with every field it covers', () => {
  const base = file.vectors.find((v) => v.name.startsWith('paymaster'));
  assert.ok(base, 'the paymaster vector is missing');
  const baseHash = toHex(hashTransactionV3(fieldsOf(base)));
  const changes: Array<[string, Partial<TxHashFields>]> = [
    ['chain id', { chainId: BigInt(base.chainId) + 1n }],
    ['nonce', { nonce: BigInt(base.nonce) + 1n }],
    ['max block height', { maxBlockHeight: BigInt(base.maxBlockHeight) + 1n }],
    ['intent expiry', { intentExpiry: BigInt(base.intentExpiry) + 1n }],
    ['intent ref', { intentRef: new Uint8Array([1]) }],
    ['value', { value: BigInt(base.value) + 1n }],
    ['data', { data: new Uint8Array([1]) }],
    ['gas limit', { gasLimit: BigInt(base.gasLimit) + 1n }],
    ['gas price', { gasPrice: BigInt(base.gasPrice) + 1n }],
    ['paymaster', { paymaster: new Uint8Array(20).fill(7) }],
    ['merchant', { merchantAddress: 'other' }],
    ['device', { deviceId: 'other' }],
    ['refund', { refundOf: 'other' }],
  ];
  for (const [name, change] of changes) {
    const h = toHex(hashTransactionV3({ ...fieldsOf(base), ...change }));
    assert.notEqual(h, baseHash, `changing the ${name} must change the digest`);
  }
});

test('inputs the node would not encode are refused', () => {
  const ok = fieldsOf(file.vectors[0]);
  assert.throws(() => hashTransactionV3({ ...ok, type: 0 }));
  assert.throws(() => hashTransactionV3({ ...ok, chainId: 1n << 64n }));
  assert.throws(() => hashTransactionV3({ ...ok, to: new Uint8Array(21) }));
  assert.throws(() => hashTransactionV3({ ...ok, value: -1n }));
});

// The SDK's own client signs what the node verifies. The node is replaced by a
// recording fake: the transaction the client sends is rebuilt into the node's digest
// and the signer is recovered from its r, s and v.
test('WalletClient signs the node digest, so the node attributes the transfer to the sender', async () => {
  const key = fromHex(file.privateKey);
  const sender = addressOf(getPublicKey(key, false));
  assert.equal(sender, file.address);

  let sent: Record<string, string> | undefined;
  const fetchImpl: typeof fetch = async (_url, init) => {
    const body = JSON.parse(String((init as RequestInit).body));
    if (body.method === 'nhb_getBalance') {
      return new Response(JSON.stringify({ result: { nonce: 5 } }), { status: 200 });
    }
    sent = body.params[0];
    return new Response(JSON.stringify({ result: 'Transaction received by node.' }), { status: 200 });
  };
  const chainId = 18346390202490284624n;
  const client = new WalletClient({ baseUrl: 'http://127.0.0.1:1', authToken: 't', fetchImpl, chainId });
  const recipient = 'nhb1qyqszqgpqyqszqgpqyqszqgpqyqszqgp6q0uya';
  const { transaction } = await client.sendTransfer({ recipient, amount: 1000n, asset: 'NHB', privateKey: key });
  assert.ok(sent, 'the transaction was not sent');

  const to = Uint8Array.from(Buffer.from(transaction.to, 'base64'));
  const digest = hashTransactionV3({
    chainId,
    type: TRANSFER_TYPE_NHB,
    nonce: BigInt(transaction.nonce),
    to,
    value: BigInt(transaction.value),
    gasLimit: BigInt(transaction.gasLimit),
    gasPrice: BigInt(transaction.gasPrice),
  });
  const recovered = new Signature(BigInt(transaction.r), BigInt(transaction.s), Number(BigInt(transaction.v) - 27n))
    .recoverPublicKey(digest)
    .toRawBytes(false);
  assert.equal(addressOf(recovered), sender, 'the node would attribute this transfer to a different address');
});
