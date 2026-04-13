import crypto from 'crypto';

import { readServerConfig } from './config';

interface RequestOptions {
  idempotencyKey?: string;
}

export interface RegisterEmailResponse {
  status: string;
  expiresIn: number;
}

export interface VerifyEmailResponse {
  status: string;
  verifiedAt: string;
  emailHash: string;
}

export interface BindEmailResponse {
  status: string;
  aliasId: string;
  emailHash: string;
  publicLookup: boolean;
}

async function gatewayRequest<T>(path: string, body: unknown, options: RequestOptions = {}): Promise<T> {
  const { identityGatewayUrl, identityGatewayKey, identityGatewaySecret } = readServerConfig();
  const url = new URL(path, identityGatewayUrl);
  const payload = JSON.stringify(body ?? {});
  const timestamp = Math.floor(Date.now() / 1000).toString();
  const bodyHash = crypto.createHash('sha256').update(payload).digest('hex');
  const message = `POST\n${url.pathname}\n${bodyHash}\n${timestamp}`;
  const signature = crypto.createHmac('sha256', identityGatewaySecret).update(message).digest('hex');
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'X-API-Key': identityGatewayKey,
    'X-API-Timestamp': timestamp,
    'X-API-Signature': signature
  };
  if (options.idempotencyKey) {
    headers['Idempotency-Key'] = options.idempotencyKey;
  }
  const response = await fetch(url.toString(), {
    method: 'POST',
    headers,
    body: payload,
    cache: 'no-store'
  });
  if (!response.ok) {
    const text = await response.text();
    const error = new Error(`Identity gateway ${response.status}: ${text}`) as Error & {
      status?: number;
      body?: string;
    };
    error.status = response.status;
    error.body = text;
    throw error;
  }
  return (await response.json()) as T;
}

export async function startEmailVerification(
  email: string,
  aliasHint?: string,
  options?: RequestOptions
): Promise<RegisterEmailResponse> {
  return gatewayRequest<RegisterEmailResponse>('/identity/email/register', { email, aliasHint }, options);
}

export async function completeEmailVerification(
  email: string,
  code: string,
  options?: RequestOptions
): Promise<VerifyEmailResponse> {
  return gatewayRequest<VerifyEmailResponse>('/identity/email/verify', { email, code }, options);
}

export async function bindEmailToAlias(
  aliasId: string,
  email: string,
  consent: boolean,
  options?: RequestOptions
): Promise<BindEmailResponse> {
  return gatewayRequest<BindEmailResponse>(
    '/identity/alias/bind-email',
    { aliasId, email, consent },
    options
  );
}
