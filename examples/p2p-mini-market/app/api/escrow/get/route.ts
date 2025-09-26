import { NextRequest, NextResponse } from 'next/server';
import { rpcRequest } from '../../../lib/rpc';

export async function GET(req: NextRequest) {
  const escrowId = req.nextUrl.searchParams.get('escrowId');
  if (!escrowId) {
    return NextResponse.json({ error: 'escrowId query parameter required' }, { status: 400 });
  }
  try {
    const result = await rpcRequest('escrow_get', [{ id: escrowId }], true);
    return NextResponse.json(result, { status: 200 });
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 500 });
  }
}
