import { NextRequest, NextResponse } from 'next/server';
import { normalizeAmount } from '../../../lib/identity';
import { rpcRequest } from '../../../lib/rpc';

interface UnstakeBody {
  caller?: string;
  creator?: string;
  amount?: string;
}

export async function POST(req: NextRequest) {
  const body = (await req.json().catch(() => ({}))) as UnstakeBody;
  const caller = body.caller?.trim();
  const creator = body.creator?.trim();
  const amountInput = body.amount?.trim() ?? '0';

  if (!caller || !creator) {
    return NextResponse.json({ error: 'caller and creator are required' }, { status: 400 });
  }

  try {
    const amount = normalizeAmount(amountInput);
    const result = await rpcRequest('creator_unstake', [
      {
        caller,
        creator,
        amount,
      },
    ], true);
    return NextResponse.json(result, { status: 200 });
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 400 });
  }
}
