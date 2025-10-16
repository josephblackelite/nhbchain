import { NextRequest, NextResponse } from 'next/server';

import EscrowDisputeClient from '../../../../../../clients/ts/escrow/dispute';
import { readServerConfig } from '../../../../lib/config';

export async function POST(req: NextRequest, { params }: { params: { id: string } }) {
  const escrowId = params?.id?.trim();
  if (!escrowId) {
    return NextResponse.json({ error: 'escrowId is required' }, { status: 400 });
  }
  let payload: { reason?: string } = {};
  if (req.body !== null) {
    try {
      payload = (await req.json()) as { reason?: string };
    } catch (error) {
      return NextResponse.json({ error: 'Invalid JSON body' }, { status: 400 });
    }
  }
  const config = readServerConfig();
  const client = new EscrowDisputeClient({ baseUrl: config.rpcUrl, authToken: config.rpcToken });
  try {
    const result = await client.dispute(escrowId, payload.reason);
    return NextResponse.json({ ok: true, result }, { status: 200 });
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 502 });
  }
}
