import { NextRequest, NextResponse } from 'next/server';
import { rpcRequest } from '../../../lib/rpc';

interface SetAliasBody {
  address?: string;
  alias?: string;
}

export async function POST(req: NextRequest) {
  const body = (await req.json().catch(() => ({}))) as SetAliasBody;
  const address = body.address?.trim();
  const alias = body.alias?.trim();
  if (!address || !alias) {
    return NextResponse.json({ error: 'address and alias are required' }, { status: 400 });
  }
  try {
    const result = await rpcRequest('identity_setAlias', [address, alias], true);
    return NextResponse.json(result, { status: 200 });
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 500 });
  }
}
