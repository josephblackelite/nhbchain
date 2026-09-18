import { NextRequest, NextResponse } from 'next/server';

interface UnstakeBody {
  caller?: string;
  creator?: string;
  amount?: string;
}

// creator_unstake is disabled server-side (410 Gone): it used to mutate
// validator-local state outside the block pipeline, which guarantees a
// consensus fork/halt on this chain's 2-validator zero-quorum-slack
// topology, and no signed-transaction replacement exists yet (see
// rpc/creator_handlers.go's creatorRPCDisabledMessage). This route returns
// the retired response itself, before ever calling the chain.
const RETIRED_MESSAGE =
  'creator_unstake is disabled -- it mutated validator-local state outside the block pipeline, guaranteeing a consensus fork/halt on a 2-validator zero-quorum-slack chain; a signed-transaction replacement is pending.';

export async function POST(req: NextRequest) {
  const body = (await req.json().catch(() => ({}))) as UnstakeBody;
  const caller = body.caller?.trim();
  const creator = body.creator?.trim();

  if (!caller || !creator) {
    return NextResponse.json({ error: 'caller and creator are required' }, { status: 400 });
  }

  return NextResponse.json(
    { error: RETIRED_MESSAGE, retired: true, method: 'creator_unstake' },
    { status: 410 },
  );
}
