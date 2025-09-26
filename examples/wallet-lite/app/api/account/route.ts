import { NextRequest, NextResponse } from 'next/server';
import { rpcRequest } from '../../lib/rpc';

export async function GET(req: NextRequest) {
  const address = req.nextUrl.searchParams.get('address');
  if (!address) {
    return NextResponse.json({ error: 'address query parameter required' }, { status: 400 });
  }
  try {
    const result = await rpcRequest('nhb_getBalance', [address]);
    return NextResponse.json(result, { status: 200 });
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 500 });
  }
}
