import { NextRequest, NextResponse } from 'next/server';
import { z } from 'zod';
import { rpcRequest } from '../../../lib/rpc';

const schema = z.object({
  tradeId: z.string().min(1),
  caller: z.string().min(1),
  outcome: z.enum(['release_both', 'refund_both', 'release_base_refund_quote', 'release_quote_refund_base']),
  memo: z.string().optional()
});

export async function POST(req: NextRequest) {
  try {
    const body = await req.json();
    const payload = schema.parse(body);
    const result = await rpcRequest('p2p_resolve', [payload], true);
    return NextResponse.json(result, { status: 200 });
  } catch (error) {
    if (error instanceof z.ZodError) {
      return NextResponse.json({ error: error.flatten() }, { status: 400 });
    }
    return NextResponse.json({ error: (error as Error).message }, { status: 500 });
  }
}
