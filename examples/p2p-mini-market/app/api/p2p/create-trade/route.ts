import { NextRequest, NextResponse } from 'next/server';
import { z } from 'zod';
import { rpcRequest } from '../../../lib/rpc';

const schema = z.object({
  offerId: z.string().min(1),
  buyer: z.string().min(1),
  seller: z.string().min(1),
  baseToken: z.string().min(1),
  baseAmount: z.string().regex(/^[0-9]+$/),
  quoteToken: z.string().min(1),
  quoteAmount: z.string().regex(/^[0-9]+$/),
  deadline: z.number().int().positive()
});

export async function POST(req: NextRequest) {
  try {
    const body = await req.json();
    const payload = schema.parse(body);
    const result = await rpcRequest('p2p_createTrade', [payload], true);
    return NextResponse.json(result, { status: 201 });
  } catch (error) {
    if (error instanceof z.ZodError) {
      return NextResponse.json({ error: error.flatten() }, { status: 400 });
    }
    return NextResponse.json({ error: (error as Error).message }, { status: 500 });
  }
}
