import { NextRequest, NextResponse } from 'next/server';
import { z } from 'zod';

const schema = z.object({
  tradeId: z.string().min(1),
  caller: z.string().min(1)
});

// p2p_settle is disabled server-side (410 Gone): it used to mutate
// validator-local state outside the block pipeline, which guarantees a
// consensus fork/halt on this chain's 2-validator zero-quorum-slack
// topology, and no signed-transaction replacement exists yet (see
// rpc/p2p_handlers.go's p2pRPCDisabledMessage). This route returns the
// retired response itself, before ever calling the chain.
const RETIRED_MESSAGE =
  'p2p_settle is disabled -- it mutated validator-local state outside the block pipeline, guaranteeing a consensus fork/halt on a 2-validator zero-quorum-slack chain; a signed-transaction replacement is pending.';

export async function POST(req: NextRequest) {
  try {
    const body = await req.json();
    schema.parse(body);
    return NextResponse.json(
      { error: RETIRED_MESSAGE, retired: true, method: 'p2p_settle' },
      { status: 410 },
    );
  } catch (error) {
    if (error instanceof z.ZodError) {
      return NextResponse.json({ error: error.flatten() }, { status: 400 });
    }
    return NextResponse.json({ error: (error as Error).message }, { status: 500 });
  }
}
