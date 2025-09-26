import { NextRequest, NextResponse } from 'next/server';
import { normalizeAmount } from '../../../lib/identity';
import { rpcRequest } from '../../../lib/rpc';

interface TipBody {
  caller?: string;
  contentId?: string;
  amount?: string;
}

export async function POST(req: NextRequest) {
  const body = (await req.json().catch(() => ({}))) as TipBody;
  const caller = body.caller?.trim();
  const contentId = body.contentId?.trim();
  const amountInput = body.amount?.trim() ?? '0';

  if (!caller || !contentId) {
    return NextResponse.json({ error: 'caller and contentId are required' }, { status: 400 });
  }

  try {
    const amount = normalizeAmount(amountInput);
    const result = await rpcRequest('creator_tip', [
      {
        caller,
        contentId,
        amount,
      },
    ], true);
    return NextResponse.json(result, { status: 200 });
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 400 });
  }
}
