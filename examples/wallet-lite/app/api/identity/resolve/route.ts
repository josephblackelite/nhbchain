import { NextRequest, NextResponse } from 'next/server';
import { rpcRequest } from '../../../lib/rpc';

export async function GET(req: NextRequest) {
  const alias = req.nextUrl.searchParams.get('alias');
  if (!alias) {
    return NextResponse.json({ error: 'alias parameter required' }, { status: 400 });
  }
  try {
    const result = await rpcRequest('identity_resolve', [alias]);
    return NextResponse.json(result, { status: 200 });
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 404 });
  }
}
