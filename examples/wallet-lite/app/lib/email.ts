import crypto from 'crypto';
import { readServerConfig } from './config';

export function computeEmailHash(email: string): string {
  const normalized = email.trim().toLowerCase();
  if (!normalized) {
    throw new Error('Email required');
  }
  const { emailSalt } = readServerConfig();
  const hmac = crypto.createHmac('sha256', emailSalt);
  hmac.update(normalized, 'utf8');
  return `0x${hmac.digest('hex')}`;
}
