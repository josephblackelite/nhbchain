import { sha256 } from '@noble/hashes/sha256';

// The digest a transaction is signed over. It is the node's own encoding, "V3", for
// every transaction whose type is above zero (Transaction.Hash in core/types):
//
//   "NHB_TX_V3_MAINNET"            ASCII, 17 bytes
//   chainId                        uint64, big-endian
//   type                           uint8
//   nonce                          uint64
//   maxBlockHeight                 uint64
//   intentExpiry                   uint64
//   intentRef                      uint16 length, bytes
//   to                             20 bytes, left-padded with zeros
//   value                          uint16 length, minimal big-endian bytes (0 is empty)
//   data                           uint32 length, bytes
//   gasLimit                       uint64
//   gasPrice                       uint16 length, minimal big-endian bytes
//   paymaster                      0x00, or 0x01 followed by 20 bytes (left-padded)
//   merchantAddress, deviceId, refundOf
//                                  each: trimmed, uint32 length, UTF-8 bytes
//
// then SHA-256 of all of it. The test vectors in test/vectors/tx-hash-vectors.json
// are produced by the node's Go code (core/types/transaction_vectors_test.go) and
// checked by test/txhash.test.ts, so a change on either side that is not made on
// the other fails a test (PL-DC-11).

export interface TxHashFields {
  chainId: bigint;
  type: number;
  nonce: bigint;
  maxBlockHeight?: bigint;
  intentExpiry?: bigint;
  intentRef?: Uint8Array;
  to: Uint8Array;
  value: bigint;
  data?: Uint8Array;
  gasLimit: bigint;
  gasPrice: bigint;
  paymaster?: Uint8Array;
  merchantAddress?: string;
  deviceId?: string;
  refundOf?: string;
}

const PREFIX = new TextEncoder().encode('NHB_TX_V3_MAINNET');
const MAX_UINT16 = 0xffff;
const MAX_UINT64 = (1n << 64n) - 1n;

class Writer {
  private readonly chunks: Uint8Array[] = [];

  bytes(data: Uint8Array): void {
    this.chunks.push(data);
  }

  uint8(value: number): void {
    if (!Number.isInteger(value) || value < 0 || value > 0xff) {
      throw new Error(`Value ${value} does not fit in one byte.`);
    }
    this.chunks.push(Uint8Array.of(value));
  }

  uint16(value: number): void {
    if (!Number.isInteger(value) || value < 0 || value > MAX_UINT16) {
      throw new Error(`Length ${value} does not fit in two bytes.`);
    }
    this.chunks.push(Uint8Array.of(value >> 8, value & 0xff));
  }

  uint32(value: number): void {
    if (!Number.isInteger(value) || value < 0 || value > 0xffffffff) {
      throw new Error(`Length ${value} does not fit in four bytes.`);
    }
    const out = new Uint8Array(4);
    new DataView(out.buffer).setUint32(0, value, false);
    this.chunks.push(out);
  }

  uint64(value: bigint, field: string): void {
    if (value < 0n || value > MAX_UINT64) {
      throw new Error(`${field} must fit in 64 bits.`);
    }
    const out = new Uint8Array(8);
    new DataView(out.buffer).setBigUint64(0, value, false);
    this.chunks.push(out);
  }

  concat(): Uint8Array {
    const total = this.chunks.reduce((n, c) => n + c.length, 0);
    const out = new Uint8Array(total);
    let offset = 0;
    for (const chunk of this.chunks) {
      out.set(chunk, offset);
      offset += chunk.length;
    }
    return out;
  }
}

// Minimal big-endian bytes of a non-negative integer; zero is the empty array, like
// Go's big.Int.Bytes().
function minimalBytes(value: bigint, field: string): Uint8Array {
  if (value < 0n) {
    throw new Error(`${field} must not be negative.`);
  }
  if (value === 0n) {
    return new Uint8Array(0);
  }
  let hex = value.toString(16);
  if (hex.length % 2 === 1) {
    hex = `0${hex}`;
  }
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i += 1) {
    out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

function padAddress(addr: Uint8Array | undefined, field: string): Uint8Array {
  const out = new Uint8Array(20);
  if (!addr || addr.length === 0) {
    return out;
  }
  if (addr.length > 20) {
    throw new Error(`${field} length must not exceed 20 bytes.`);
  }
  out.set(addr, 20 - addr.length);
  return out;
}

function lengthPrefixedNumber(w: Writer, value: bigint, field: string): void {
  const bytes = minimalBytes(value, field);
  w.uint16(bytes.length);
  w.bytes(bytes);
}

function trimmedString(w: Writer, value: string | undefined): void {
  const bytes = new TextEncoder().encode((value ?? '').trim());
  w.uint32(bytes.length);
  w.bytes(bytes);
}

/** The 32-byte digest the node verifies a transaction's signature against. */
export function hashTransactionV3(tx: TxHashFields): Uint8Array {
  if (!Number.isInteger(tx.type) || tx.type <= 0) {
    throw new Error('Only transactions with a type above zero use the V3 digest.');
  }
  const w = new Writer();
  w.bytes(PREFIX);
  w.uint64(tx.chainId, 'chain id');
  w.uint8(tx.type);
  w.uint64(tx.nonce, 'nonce');
  w.uint64(tx.maxBlockHeight ?? 0n, 'max block height');
  w.uint64(tx.intentExpiry ?? 0n, 'intent expiry');
  const intentRef = tx.intentRef ?? new Uint8Array(0);
  if (intentRef.length > MAX_UINT16) {
    throw new Error('Intent reference is too long.');
  }
  w.uint16(intentRef.length);
  w.bytes(intentRef);
  w.bytes(padAddress(tx.to, 'to'));
  lengthPrefixedNumber(w, tx.value, 'value');
  const data = tx.data ?? new Uint8Array(0);
  w.uint32(data.length);
  w.bytes(data);
  w.uint64(tx.gasLimit, 'gas limit');
  lengthPrefixedNumber(w, tx.gasPrice, 'gas price');
  if (tx.paymaster && tx.paymaster.length > 0) {
    w.uint8(1);
    w.bytes(padAddress(tx.paymaster, 'paymaster'));
  } else {
    w.uint8(0);
  }
  trimmedString(w, tx.merchantAddress);
  trimmedString(w, tx.deviceId);
  trimmedString(w, tx.refundOf);
  return sha256(w.concat());
}
