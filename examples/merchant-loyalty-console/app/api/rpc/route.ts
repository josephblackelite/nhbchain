import { NextRequest, NextResponse } from 'next/server';

const DEFAULT_RPC = 'https://api.nhbcoin.net/rpc';

const rpcUrl = (process.env.NHB_RPC_URL || DEFAULT_RPC).trim().replace(/\/$/, '');
const rpcToken = process.env.NHB_RPC_TOKEN?.trim();

export async function POST(req: NextRequest) {
  try {
    const body = await req.json();
    const { method, params, id, useAuth = true } = body ?? {};

    if (typeof method !== 'string' || method.length === 0) {
      return NextResponse.json(
        { error: { message: 'RPC method is required' } },
        { status: 400 }
      );
    }

    const payload = {
      jsonrpc: '2.0',
      id: typeof id === 'number' || typeof id === 'string' ? id : Date.now(),
      method,
      params: Array.isArray(params) ? params : params ? [params] : []
    };

    const headers: Record<string, string> = {
      'Content-Type': 'application/json'
    };
    if (rpcToken && useAuth !== false) {
      headers.Authorization = `Bearer ${rpcToken}`;
    }

    const response = await fetch(rpcUrl, {
      method: 'POST',
      headers,
      body: JSON.stringify(payload),
      cache: 'no-store'
    });

    const text = await response.text();
    let data: unknown;
    try {
      data = text ? JSON.parse(text) : {};
    } catch (error) {
      return NextResponse.json(
        {
          error: {
            message: 'Failed to decode RPC response',
            detail: (error as Error).message,
            raw: text
          }
        },
        { status: 502 }
      );
    }

    if (!response.ok) {
      return NextResponse.json(data, { status: response.status });
    }

    return NextResponse.json(data);
  } catch (error) {
    return NextResponse.json(
      {
        error: {
          message: 'RPC proxy error',
          detail: error instanceof Error ? error.message : String(error)
        }
      },
      { status: 500 }
    );
  }
}
