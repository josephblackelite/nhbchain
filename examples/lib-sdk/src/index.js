import crypto from 'crypto';
import { bech32 } from '@scure/base';
import { sign, utils } from '@noble/secp256k1';

export const API_KEY_HEADER = 'x-nhb-api-key';
export const SIGNATURE_HEADER = 'x-nhb-signature';
export const TIMESTAMP_HEADER = 'x-nhb-timestamp';
export const CHAIN_ID_HEADER = 'x-nhb-chain-id';
export const IDEMPOTENCY_HEADER = 'x-nhb-idempotency-key';

const DEFAULT_TIMEOUT = 10_000;

function assertFetch(fetchImpl) {
  if (typeof fetchImpl !== 'function') {
    throw new Error('A fetch implementation must be provided.');
  }
  return fetchImpl;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function isRetriable(error) {
  if (!error) return false;
  if (error.name === 'AbortError') return false;
  return true;
}

function normalizePrivateKey(privateKey) {
  if (!privateKey) {
    throw new Error('A private key is required to sign the message.');
  }
  const trimmed = privateKey.startsWith('0x') ? privateKey.slice(2) : privateKey;
  if (trimmed.length !== 64) {
    throw new Error('Expected a 32-byte hex private key.');
  }
  return trimmed;
}

function computeBody(body) {
  return typeof body === 'string' ? body : JSON.stringify(body);
}

/**
 * Generates a deterministic HMAC signature for authenticated requests.
 *
 * The signature covers the request body and a timestamp to prevent replay attacks.
 * The resulting string is encoded as lowercase hexadecimal.
 *
 * @param {string|object} body - The request payload that will be sent to the gateway.
 * @param {string} secret - Shared HMAC secret obtained from the NHB gateway.
 * @param {string} [timestamp] - ISO-8601 timestamp. Defaults to `new Date().toISOString()`.
 * @returns {string} Hex encoded HMAC digest of the body and timestamp.
 */
export function hmacSign(body, secret, timestamp = new Date().toISOString()) {
  if (!secret) {
    throw new Error('An HMAC secret is required to compute signatures.');
  }
  const buffer = Buffer.from(`${timestamp}:${computeBody(body)}`);
  const key = secret.startsWith('0x') && secret.length % 2 === 0
    ? Buffer.from(secret.slice(2), 'hex')
    : Buffer.from(secret, 'utf8');
  return crypto.createHmac('sha256', key).update(buffer).digest('hex');
}

/**
 * Signs an arbitrary UTF-8 message with a secp256k1 private key.
 *
 * This helper is useful for wallet authentication flows or message signing demos.
 * The message is hashed with SHA-256 before signing and the resulting signature is
 * encoded as a lowercase hex string.
 *
 * @param {string|Uint8Array} message - Message to sign. Strings are encoded as UTF-8.
 * @param {string} privateKey - Hex encoded secp256k1 private key (0x prefix optional).
 * @returns {Promise<string>} Hex encoded signature (64 bytes).
 */
export async function walletSig(message, privateKey) {
  const keyHex = normalizePrivateKey(privateKey);
  const msgBytes = typeof message === 'string' ? utils.stringToBytes(message) : new Uint8Array(message);
  const digest = utils.sha256(msgBytes);
  const signature = await sign(digest, keyHex, { der: false });
  return utils.bytesToHex(signature);
}

/**
 * Creates RPC client bound to a specific NHB gateway.
 *
 * Handles HMAC authentication, retry semantics, and idempotency headers out of the box.
 * Consumers can override the fetch implementation and retry behaviour through options.
 *
 * @param {object} options
 * @param {string} options.baseUrl - RPC endpoint URL.
 * @param {string} [options.apiKey] - Gateway API key.
 * @param {string} [options.apiSecret] - Gateway API secret used for HMAC signatures.
 * @param {string} [options.chainId] - NHB chain identifier attached to requests.
 * @param {number} [options.maxRetries=2] - Number of retries for transient failures.
 * @param {function} [options.fetchImpl] - Custom fetch implementation (defaults to global fetch).
 * @returns {{ request(method: string, params?: any[], requestOptions?: object): Promise<any> }}
 */
export function rpcClient({
  baseUrl,
  apiKey,
  apiSecret,
  chainId,
  maxRetries = 2,
  fetchImpl = globalThis.fetch,
} = {}) {
  if (!baseUrl) {
    throw new Error('`baseUrl` is required to create an RPC client.');
  }
  const fetchFn = assertFetch(fetchImpl);

  return {
    /**
     * Performs a JSON-RPC request with automatic retries and HMAC authentication.
     *
     * @param {string} method - RPC method.
     * @param {any[]} [params=[]] - RPC params.
     * @param {object} [requestOptions]
     * @param {string} [requestOptions.id] - Custom RPC id. Defaults to a UUID.
     * @param {AbortSignal} [requestOptions.signal] - Abort signal to cancel the request.
     * @param {Record<string,string>} [requestOptions.headers] - Additional headers.
     * @param {string} [requestOptions.idempotencyKey] - Optional idempotency key value.
     * @param {number} [requestOptions.timeout=DEFAULT_TIMEOUT] - Per-request timeout in milliseconds.
     * @param {number} [requestOptions.retries=maxRetries] - Override retry count for this call.
     * @returns {Promise<any>} Resolves with the RPC result or rejects with an Error.
     */
    async request(method, params = [], requestOptions = {}) {
      const {
        id = crypto.randomUUID(),
        signal,
        headers = {},
        idempotencyKey,
        timeout = DEFAULT_TIMEOUT,
        retries = maxRetries,
      } = requestOptions;

      const controller = new AbortController();
      if (signal) {
        if (signal.aborted) {
          throw signal.reason ?? new Error('Request aborted before execution.');
        }
        signal.addEventListener('abort', () => controller.abort(signal.reason), { once: true });
      }

      const timer = setTimeout(() => controller.abort(new Error('RPC request timed out')), timeout);

      const body = {
        jsonrpc: '2.0',
        id,
        method,
        params,
      };

      let attempt = 0;
      let lastError;
      while (attempt <= retries) {
        const timestamp = new Date().toISOString();
        const payload = computeBody(body);
        const computedHeaders = {
          'content-type': 'application/json',
          [TIMESTAMP_HEADER]: timestamp,
          [IDEMPOTENCY_HEADER]: idempotencyKey ?? createIdempotencyKey(),
          ...headers,
        };

        if (apiKey) {
          computedHeaders[API_KEY_HEADER] = apiKey;
        }
        if (chainId) {
          computedHeaders[CHAIN_ID_HEADER] = chainId;
        }
        if (apiSecret) {
          computedHeaders[SIGNATURE_HEADER] = hmacSign(payload, apiSecret, timestamp);
        }

        try {
          const response = await fetchFn(baseUrl, {
            method: 'POST',
            headers: computedHeaders,
            body: payload,
            signal: controller.signal,
          });

          if (!response.ok) {
            throw new Error(`RPC request failed with status ${response.status}`);
          }
          const json = await response.json();
          if (json.error) {
            const err = new Error(json.error.message || 'RPC returned an error');
            err.code = json.error.code;
            err.data = json.error.data;
            throw err;
          }
          clearTimeout(timer);
          return json.result;
        } catch (error) {
          lastError = error;
          const shouldRetry = attempt < retries && isRetriable(error);
          if (!shouldRetry) {
            clearTimeout(timer);
            throw error;
          }
          await delay(2 ** attempt * 200);
        }
        attempt += 1;
      }

      clearTimeout(timer);
      throw lastError ?? new Error('RPC request failed without a specific error.');
    },
  };
}

/**
 * Generates a RFC4122 idempotency key.
 *
 * Include this header on mutating requests so the gateway can dedupe retries.
 *
 * @returns {string} UUID v4 value suitable for the idempotency header.
 */
export function createIdempotencyKey() {
  return crypto.randomUUID();
}

/**
 * Builds the idempotency header map that can be merged into a request.
 *
 * @param {string} [key] - Existing key. When omitted, a new key is generated.
 * @returns {{ [IDEMPOTENCY_HEADER]: string }} Header map containing the idempotency key.
 */
export function idempotencyHeader(key = createIdempotencyKey()) {
  return { [IDEMPOTENCY_HEADER]: key };
}

export const bech32Helpers = {
  /**
   * Encodes raw bytes into a Bech32 address with the provided prefix.
   * @param {string} prefix
   * @param {Uint8Array} data
   * @returns {string}
   */
  encode(prefix, data) {
    return bech32.encode(prefix, bech32.toWords(data));
  },
  /**
   * Decodes a Bech32 string into prefix and bytes.
   * @param {string} address
   * @returns {{ prefix: string, data: Uint8Array }}
   */
  decode(address) {
    const { prefix, words } = bech32.decode(address);
    return { prefix, data: bech32.fromWords(words) };
  },
  toWords: bech32.toWords,
  fromWords: bech32.fromWords,
};

export default {
  API_KEY_HEADER,
  SIGNATURE_HEADER,
  TIMESTAMP_HEADER,
  CHAIN_ID_HEADER,
  IDEMPOTENCY_HEADER,
  rpcClient,
  hmacSign,
  walletSig,
  bech32Helpers,
  createIdempotencyKey,
  idempotencyHeader,
};
