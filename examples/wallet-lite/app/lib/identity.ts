import { keccak_256 } from '@noble/hashes/sha3';
import { utils } from '@noble/secp256k1';

export function deriveAliasId(alias: string): string {
  const normalized = alias.trim().toLowerCase();
  const hash = keccak_256(normalized);
  return `0x${utils.bytesToHex(hash)}`;
}

export function aliasFingerprint(address: string): string {
  const trimmed = address.trim();
  if (trimmed.length <= 12) {
    return trimmed;
  }
  return `${trimmed.slice(0, 6)}…${trimmed.slice(-6)}`;
}

export function normalizeAmount(amount: string): string {
  const trimmed = amount.trim();
  if (trimmed === '') {
    throw new Error('Amount required');
  }
  const [wholePart, fractional = ''] = trimmed.split('.');
  if (!/^\d*$/.test(wholePart) || !/^\d*$/.test(fractional)) {
    throw new Error('Amount must be numeric');
  }
  if (fractional.length > 18) {
    throw new Error('Maximum precision is 18 decimals');
  }
  const whole = wholePart === '' ? '0' : wholePart.replace(/^0+(\d)/, '$1');
  const paddedFraction = fractional.padEnd(18, '0');
  const combined = `${whole}${paddedFraction}`.replace(/^0+(\d)/, '$1');
  return combined === '' ? '0' : combined;
}

export function formatDeadline(hoursFromNow: number): number {
  const now = Math.floor(Date.now() / 1000);
  const delta = Math.max(1, Math.floor(hoursFromNow * 3600));
  return now + delta;
}
