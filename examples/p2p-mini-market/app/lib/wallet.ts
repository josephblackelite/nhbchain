import { keccak_256 } from '@noble/hashes/sha3';
import { utils, getPublicKey } from '@noble/secp256k1';
import { bech32Helpers } from '@nhb/examples-lib-sdk';

export function deriveAddressFromPrivateKey(hexKey: string, prefix = 'nhb'): string {
  const normalized = hexKey.trim().toLowerCase().replace(/^0x/, '');
  if (normalized.length !== 64) {
    throw new Error('Expected 32-byte private key');
  }
  const keyBytes = utils.hexToBytes(normalized);
  const pubKey = getPublicKey(keyBytes, false).slice(1);
  const hash = keccak_256(pubKey);
  const addressBytes = hash.slice(-20);
  return bech32Helpers.encode(prefix, addressBytes);
}

export function randomPrivateKey(): string {
  const keyBytes = utils.randomPrivateKey();
  return `0x${utils.bytesToHex(keyBytes)}`;
}
