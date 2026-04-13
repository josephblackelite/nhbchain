import { NextRequest, NextResponse } from 'next/server';
import { rpcRequest } from '../../../lib/rpc';

export async function GET(req: NextRequest) {
  const tradeId = req.nextUrl.searchParams.get('tradeId');
  if (!tradeId) {
    return NextResponse.json({ error: 'tradeId query parameter required' }, { status: 400 });
  }
  try {
    const result = await rpcRequest('p2p_getTrade', [{ tradeId }], true);
    return NextResponse.json(result, { status: 200 });
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 500 });
  }
}
