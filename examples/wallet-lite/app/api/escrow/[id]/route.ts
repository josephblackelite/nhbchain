import { NextRequest, NextResponse } from 'next/server';

import { readServerConfig } from '../../../lib/config';

function buildGatewayUrl(base: string, path: string): string {
  const normalised = base.endsWith('/') ? base : `${base}/`;
  return new URL(path, normalised).toString();
}

export async function GET(_req: NextRequest, { params }: { params: { id: string } }) {
  const escrowId = params?.id?.trim();
  if (!escrowId) {
    return NextResponse.json({ error: 'escrowId is required' }, { status: 400 });
  }
  const config = readServerConfig();
  const target = buildGatewayUrl(config.rpcUrl, `wallet/escrows/${encodeURIComponent(escrowId)}`);
  const headers: Record<string, string> = { 'X-Chain-Id': config.chainId };
  try {
    const response = await fetch(target, { headers });
    const text = await response.text();
    let payload: unknown;
    try {
      payload = text ? JSON.parse(text) : {};
    } catch (error) {
      return NextResponse.json(
        { error: `Failed to parse gateway response: ${(error as Error).message}` },
        { status: 502 },
      );
    }
    if (!response.ok) {
      const errorMessage = (payload as { error?: string })?.error ?? 'Failed to load escrow details';
      return NextResponse.json({ error: errorMessage }, { status: response.status });
    }
    return NextResponse.json(payload, { status: 200 });
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 502 });
  }
}
