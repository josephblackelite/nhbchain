import { bech32 } from '@scure/base';
import { keccak_256 } from '@noble/hashes/sha3';
import { getPublicKey, signAsync } from '@noble/secp256k1';

import { hashTransactionV3 } from './txhash.js';

export const TRANSFER_TYPE_NHB = 0x01;
export const TRANSFER_TYPE_ZNHB = 0x10;

const DEFAULT_GAS_LIMIT = 25_000n;
const DEFAULT_GAS_PRICE = 1n;
// How long an id read from the node is reused. The node reports one id until the chain's activation height and
// another from it, so the answer must not be kept for long (ledger PL-R5T-29).
export const CHAIN_ID_REUSE_MS = 15_000;
const MAX_UINT64 = (1n << 64n) - 1n;

/**
 * Reads the chain id out of the `txChainId` field of an `nhb_getNetworkStats` result. The node serves a decimal
 * string; a bigint or a safe integer is also accepted. Anything else (missing, empty, zero, negative, hex, a
 * fraction, a number past 2^53 that a JSON parser has already rounded, more than a uint64) is refused.
 */
function parseTxChainId(value: unknown): bigint {
  let id: bigint;
  if (typeof value === 'bigint') {
    id = value;
  } else if (typeof value === 'number' && Number.isSafeInteger(value)) {
    id = BigInt(value);
  } else if (typeof value === 'string' && /^[0-9]+$/.test(value.trim())) {
    id = BigInt(value.trim());
  } else {
    throw new Error('the node reported no usable txChainId');
  }
  if (id <= 0n || id > MAX_UINT64) {
    throw new Error(`the node reported a txChainId that is not a positive uint64 (${String(id)})`);
  }
  return id;
}

function assertFetch(fetchImpl: typeof fetch | undefined): asserts fetchImpl {
  if (typeof fetchImpl !== 'function') {
    throw new Error('A fetch implementation must be provided.');
  }
}

function toBase64(data: Uint8Array): string {
  if (typeof Buffer !== 'undefined') {
    return Buffer.from(data).toString('base64');
  }
  let binary = '';
  for (const byte of data) {
    binary += String.fromCharCode(byte);
  }
  if (typeof btoa === 'function') {
    return btoa(binary);
  }
  throw new Error('No base64 encoder available in this environment.');
}

function fromBech32(address: string): Uint8Array {
  let decoded;
  try {
    decoded = bech32.decode(address);
  } catch (error) {
    throw new Error('Invalid NHB address.');
  }
  if (decoded.prefix !== 'nhb') {
    throw new Error('Invalid NHB address.');
  }
  const data = bech32.fromWords(decoded.words);
  if (data.length !== 20) {
    throw new Error('Expected a 20-byte address.');
  }
  return Uint8Array.from(data);
}

function toBech32(prefix: string, data: Uint8Array): string {
  return bech32.encode(prefix, bech32.toWords(data));
}

function normalizePrivateKey(privateKey: string | Uint8Array): Uint8Array {
  if (privateKey instanceof Uint8Array) {
    if (privateKey.length !== 32) {
      throw new Error('Private key must be 32 bytes.');
    }
    return new Uint8Array(privateKey);
  }
  const trimmed = privateKey.trim().toLowerCase().replace(/^0x/, '');
  if (trimmed.length !== 64) {
    throw new Error('Expected a 32-byte hex private key.');
  }
  return hexToBytes(trimmed);
}

function hexToBytes(hex: string): Uint8Array {
  if (hex.length % 2 !== 0) {
    throw new Error('Hex string must have an even length.');
  }
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < bytes.length; i += 1) {
    const byte = hex.slice(i * 2, i * 2 + 2);
    const parsed = Number.parseInt(byte, 16);
    if (Number.isNaN(parsed)) {
      throw new Error('Invalid hex character in private key.');
    }
    bytes[i] = parsed;
  }
  return bytes;
}

function deriveAddress(privateKey: Uint8Array): Uint8Array {
  const pub = getPublicKey(privateKey, false).slice(1);
  const hashed = keccak_256(pub);
  return hashed.slice(-20);
}

function toDecimal(value: bigint): string {
  return value.toString(10);
}

function normalizeBigInt(value: bigint | string | number, label: string): bigint {
  if (typeof value === 'bigint') {
    return value;
  }
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value)) {
      throw new Error(`${label} must be a safe integer.`);
    }
    return BigInt(value);
  }
  const trimmed = value.trim();
  if (trimmed === '') {
    throw new Error(`${label} is required.`);
  }
  return BigInt(trimmed);
}

interface RpcError {
  code: number;
  message: string;
  data?: unknown;
}

interface RpcResponse<T> {
  result: T;
  error?: RpcError;
}

interface BalanceResponse {
  nonce: number;
}

export interface WalletClientOptions {
  baseUrl: string;
  authToken?: string;
  fetchImpl?: typeof fetch;
  /**
   * An explicit chain id: every transfer carries it and the node is not asked. Without it the client reads the id the
   * node reports (`nhb_getNetworkStats`, field `txChainId`) and refuses to sign when the node reports none. There is no
   * built-in default.
   */
  chainId?: bigint | string | number;
  /** How long an id read from the node is reused, in milliseconds (default 15000). */
  chainIdTtlMs?: number;
  /** Clock used for that expiry, in milliseconds (default `Date.now`); for tests. */
  now?: () => number;
  gasLimit?: bigint | string | number;
  gasPrice?: bigint | string | number;
}

export interface SendTransferOptions {
  recipient: string;
  amount: bigint | string | number;
  privateKey: string | Uint8Array;
  asset?: 'NHB' | 'ZNHB';
  gasLimit?: bigint | string | number;
  gasPrice?: bigint | string | number;
}

export interface SignedTransaction {
  chainId: string;
  type: number;
  nonce: string;
  to: string;
  value: string;
  data: string;
  gasLimit: string;
  gasPrice: string;
  r: string;
  s: string;
  v: string;
}

export interface SendTransferResult {
  transaction: SignedTransaction;
  response: string;
}

export class WalletClient {
  private readonly baseUrl: string;
  private readonly authToken?: string;
  private readonly fetchImpl: typeof fetch;
  private readonly explicitChainId?: bigint;
  private readonly chainIdTtlMs: number;
  private readonly now: () => number;
  private cachedChainId?: { value: bigint; at: number };
  private inflightChainId?: Promise<bigint>;
  private readonly defaultGasLimit: bigint;
  private readonly defaultGasPrice: bigint;

  constructor(options: WalletClientOptions) {
    if (!options || !options.baseUrl) {
      throw new Error('`baseUrl` is required to create a wallet client.');
    }
    this.baseUrl = options.baseUrl;
    this.authToken = options.authToken?.trim();
    this.fetchImpl = options.fetchImpl ?? globalThis.fetch;
    assertFetch(this.fetchImpl);
    this.explicitChainId =
      options.chainId === undefined ? undefined : normalizeBigInt(options.chainId, 'chain id');
    this.chainIdTtlMs = options.chainIdTtlMs ?? CHAIN_ID_REUSE_MS;
    if (!Number.isFinite(this.chainIdTtlMs) || this.chainIdTtlMs <= 0) {
      throw new Error('The chain id expiry must be a positive number of milliseconds.');
    }
    this.now = options.now ?? (() => Date.now());
    this.defaultGasLimit = normalizeBigInt(options.gasLimit ?? DEFAULT_GAS_LIMIT, 'gas limit');
    this.defaultGasPrice = normalizeBigInt(options.gasPrice ?? DEFAULT_GAS_PRICE, 'gas price');
    if (this.defaultGasLimit <= 0n) {
      throw new Error('Gas limit must be greater than zero.');
    }
    if (this.defaultGasPrice <= 0n) {
      throw new Error('Gas price must be greater than zero.');
    }
  }

  /**
   * The chain id a transaction signed now must carry: the explicit `chainId` option, or else the id the node reports
   * in `nhb_getNetworkStats` (public; `net_info` is operator-only), reused for a short time. It rejects, never
   * defaults, when the node cannot be asked or reports no usable id.
   */
  async txChainId(): Promise<bigint> {
    if (this.explicitChainId !== undefined) {
      return this.explicitChainId;
    }
    const started = this.now();
    if (this.cachedChainId && started - this.cachedChainId.at < this.chainIdTtlMs) {
      return this.cachedChainId.value;
    }
    if (this.inflightChainId) {
      return this.inflightChainId;
    }
    const read = (async () => {
      try {
        const stats = await this.rpc<{ txChainId?: unknown } | null>('nhb_getNetworkStats', [], false);
        const value = parseTxChainId(stats?.txChainId);
        this.cachedChainId = { value, at: started };
        return value;
      } catch (error) {
        this.cachedChainId = undefined;
        const reason = error instanceof Error ? error.message : String(error);
        throw new Error(`Cannot read the transaction chain id from the node (nhb_getNetworkStats): ${reason}`);
      } finally {
        this.inflightChainId = undefined;
      }
    })();
    this.inflightChainId = read;
    return read;
  }

  async sendTransfer(options: SendTransferOptions): Promise<SendTransferResult> {
    const asset = options.asset ?? 'ZNHB';
    const type = asset === 'NHB' ? TRANSFER_TYPE_NHB : TRANSFER_TYPE_ZNHB;
    const amount = normalizeBigInt(options.amount, 'amount');
    if (amount <= 0n) {
      throw new Error('Transfer amount must be positive.');
    }
    const privateKey = normalizePrivateKey(options.privateKey);
    const recipientBytes = fromBech32(options.recipient);
    const senderAddress = toBech32('nhb', deriveAddress(privateKey));
    const chainId = await this.txChainId();
    const nonceInfo = await this.rpc<BalanceResponse>('nhb_getBalance', [senderAddress], false);
    const nonce = BigInt(nonceInfo?.nonce ?? 0);
    const gasLimit = normalizeBigInt(options.gasLimit ?? this.defaultGasLimit, 'gas limit');
    const gasPrice = normalizeBigInt(options.gasPrice ?? this.defaultGasPrice, 'gas price');
    if (gasLimit <= 0n) {
      throw new Error('Gas limit must be greater than zero.');
    }
    if (gasPrice <= 0n) {
      throw new Error('Gas price must be greater than zero.');
    }

    // The digest is the node's own binary encoding of the transaction (Transaction.Hash
    // in core/types), not a JSON text: a signature over any other digest is attributed
    // by the node to a different, unfunded address (PL-DC-11).
    const digest = hashTransactionV3({
      chainId,
      type,
      nonce,
      to: recipientBytes,
      value: amount,
      data: new Uint8Array(0),
      gasLimit,
      gasPrice,
    });
    const signature = await signAsync(digest, privateKey);
    const r = signature.r;
    const s = signature.s;
    const recovery = signature.recovery ?? 0;
    const v = BigInt(recovery + 27);

    const signed: SignedTransaction = {
      chainId: toDecimal(chainId),
      type,
      nonce: toDecimal(nonce),
      to: toBase64(recipientBytes),
      value: toDecimal(amount),
      data: toBase64(new Uint8Array(0)),
      gasLimit: toDecimal(gasLimit),
      gasPrice: toDecimal(gasPrice),
      r: toDecimal(r),
      s: toDecimal(s),
      v: toDecimal(v),
    };

    const payload = buildSendPayload(signed);
    const response = await this.rpcWithRawParam('nhb_sendTransaction', payload, true);
    return { transaction: signed, response };
  }

  private async rpc<T>(method: string, params: unknown[], requireAuth: boolean): Promise<T> {
    const body = JSON.stringify({ jsonrpc: '2.0', id: 1, method, params });
    return this.performRequest<T>(body, method, requireAuth);
  }

  private async rpcWithRawParam<T>(method: string, rawParam: string, requireAuth: boolean): Promise<T> {
    const body = `{"jsonrpc":"2.0","id":1,"method":"${method}","params":[${rawParam}]}`;
    return this.performRequest<T>(body, method, requireAuth);
  }

  private async performRequest<T>(body: string, method: string, requireAuth: boolean): Promise<T> {
    if (requireAuth && !this.authToken) {
      throw new Error(`Method ${method} requires an authorization token.`);
    }
    const headers: Record<string, string> = { 'content-type': 'application/json' };
    if (requireAuth && this.authToken) {
      headers['authorization'] = `Bearer ${this.authToken}`;
    }
    const response = await this.fetchImpl(this.baseUrl, {
      method: 'POST',
      body,
      headers,
    });
    if (!response.ok) {
      throw new Error(`RPC request failed with status ${response.status}`);
    }
    const json: RpcResponse<T> = await response.json();
    if (json.error) {
      throw new Error(json.error.message || 'RPC returned an error');
    }
    return json.result;
  }
}

function buildSendPayload(tx: SignedTransaction): string {
  const parts = [
    `"chainId":${tx.chainId}`,
    `"type":${tx.type}`,
    `"nonce":${tx.nonce}`,
    `"to":${JSON.stringify(tx.to)}`,
    `"value":${tx.value}`,
    `"data":${JSON.stringify(tx.data)}`,
    `"gasLimit":${tx.gasLimit}`,
    `"gasPrice":${tx.gasPrice}`,
    `"r":${tx.r}`,
    `"s":${tx.s}`,
    `"v":${tx.v}`,
  ];
  return `{${parts.join(',')}}`;
}

export default WalletClient;
