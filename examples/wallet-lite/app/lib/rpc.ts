import crypto from 'crypto';
import { CHAIN_ID_HEADER } from '@nhb/examples-lib-sdk';
import { readServerConfig } from './config';

export async function rpcRequest<T>(method: string, params: unknown[], withAuth = false): Promise<T> {
  const config = readServerConfig();
  const body = {
    jsonrpc: '2.0',
    id: crypto.randomUUID(),
    method,
    params
  };
  const headers: Record<string, string> = {
    'content-type': 'application/json',
    [CHAIN_ID_HEADER]: config.chainId
  };
  if (withAuth) {
    headers.Authorization = `Bearer ${config.rpcToken}`;
  }
  const response = await fetch(config.rpcUrl, {
    method: 'POST',
    headers,
    body: JSON.stringify(body)
  });
  if (!response.ok) {
    const text = await response.text();
    throw new Error(`RPC error (${response.status}): ${text}`);
  }
  const json = await response.json();
  if (json.error) {
    const message = json.error?.message ?? 'unknown error';
    throw new Error(message);
  }
  return json.result as T;
}
