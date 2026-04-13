'use client';

export interface JsonRpcError {
  code?: number;
  message: string;
  data?: unknown;
}

export interface JsonRpcResponse<T> {
  jsonrpc: string;
  id: number | string;
  result?: T;
  error?: JsonRpcError;
}

export class RpcError extends Error {
  code?: number;
  data?: unknown;

  constructor(message: string, code?: number, data?: unknown) {
    super(message);
    this.code = code;
    this.data = data;
  }
}

export async function rpcCall<T>(
  method: string,
  params?: unknown[] | Record<string, unknown>,
  options?: { auth?: boolean }
): Promise<T> {
  const body = {
    method,
    params,
    useAuth: options?.auth !== false
  };

  const response = await fetch('/api/rpc', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json'
    },
    body: JSON.stringify(body)
  });

  const payload: JsonRpcResponse<T> = await response.json();
  const rpcError = payload.error;

  if (rpcError) {
    throw new RpcError(rpcError.message || 'Unknown RPC error', rpcError.code, rpcError.data);
  }

  if (!response.ok) {
    throw new RpcError(`RPC HTTP error (${response.status})`);
  }

  return payload.result as T;
}

export function formatAmount(amount: string | number | null | undefined): string {
  if (!amount) return '0';
  const value = typeof amount === 'string' ? amount : amount.toString();
  return value.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

export function toUnixSeconds(value: string): number | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return undefined;
  }
  return Math.floor(date.getTime() / 1000);
}

export function fromUnixSeconds(value: number | string | null | undefined): string {
  if (!value) return '';
  const num = typeof value === 'string' ? Number(value) : value;
  if (!Number.isFinite(num) || num <= 0) {
    return '';
  }
  const date = new Date(num * 1000);
  return date.toISOString().slice(0, 16);
}
