import { NextRequest, NextResponse } from 'next/server';

interface SetAliasBody {
  address?: string;
  alias?: string;
}

// identity_setAlias is disabled server-side (410 Gone): it used to mutate
// validator-local state outside the block pipeline, which guarantees a
// consensus fork/halt on this chain's 2-validator zero-quorum-slack
// topology, and no signed-transaction replacement exists yet (see
// rpc/identity_handlers.go's identityRPCDisabledMessage). This route returns
// the retired response itself, before ever calling the chain, so the
// failure is instant and self-explanatory instead of a generic upstream
// error.
const RETIRED_MESSAGE =
  'identity_setAlias is disabled -- it mutated validator-local state outside the block pipeline, guaranteeing a consensus fork/halt on a 2-validator zero-quorum-slack chain; a signed-transaction replacement is pending.';

export async function POST(req: NextRequest) {
  const body = (await req.json().catch(() => ({}))) as SetAliasBody;
  const address = body.address?.trim();
  const alias = body.alias?.trim();
  if (!address || !alias) {
    return NextResponse.json({ error: 'address and alias are required' }, { status: 400 });
  }
  return NextResponse.json(
    { error: RETIRED_MESSAGE, retired: true, method: 'identity_setAlias' },
    { status: 410 },
  );
}
