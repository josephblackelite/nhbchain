import crypto from 'node:crypto';

export interface WebhookEvent<T = unknown> {
  type: string;
  data: T;
  id: string;
  created_at: string;
}

const encoder = new TextEncoder();

function timingSafeEqual(a: string, b: string): boolean {
  const aBytes = encoder.encode(a);
  const bBytes = encoder.encode(b);
  if (aBytes.length !== bBytes.length) {
    return false;
  }
  return crypto.timingSafeEqual(Buffer.from(aBytes), Buffer.from(bBytes));
}

export function createWebhookVerifier(secret: string) {
  return (payload: Buffer, signature?: string | null, timestamp?: string | null): boolean => {
    if (!signature || !timestamp) return false;
    const expected = crypto.createHmac('sha256', secret).update(`${timestamp}.${payload.toString('utf8')}`).digest('hex');
    return timingSafeEqual(expected, signature);
  };
}
