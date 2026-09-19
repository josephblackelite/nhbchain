import { NextRequest, NextResponse } from 'next/server';
import { deriveAliasId } from '../../../lib/identity';

interface ClaimBody {
  claimId?: string;
  payee?: string;
  preimage?: string;
  alias?: string;
}

// identity_claim is disabled server-side (410 Gone): it used to mutate
// validator-local state outside the block pipeline, which guarantees a
// consensus fork/halt on this chain's 2-validator zero-quorum-slack
// topology, and no signed-transaction replacement exists yet (see
// rpc/identity_handlers.go's identityRPCDisabledMessage). This route returns
// the retired response itself, before ever calling the chain, so the
// failure is instant and self-explanatory instead of a generic upstream
// error.
const RETIRED_MESSAGE =
  'identity_claim is disabled -- it mutated validator-local state outside the block pipeline, guaranteeing a consensus fork/halt on a 2-validator zero-quorum-slack chain; a signed-transaction replacement is pending.';

export async function POST(req: NextRequest) {
  const body = (await req.json().catch(() => ({}))) as ClaimBody;
  const claimId = body.claimId?.trim();
  const payee = body.payee?.trim();
  if (!claimId || !payee) {
    return NextResponse.json({ error: 'claimId and payee are required' }, { status: 400 });
  }
  try {
    let preimage = body.preimage?.trim();
    if (!preimage && body.alias) {
      preimage = deriveAliasId(body.alias);
    }
    if (!preimage) {
      throw new Error('preimage or alias required');
    }
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 400 });
  }
  return NextResponse.json(
    { error: RETIRED_MESSAGE, retired: true, method: 'identity_claim' },
    { status: 410 },
  );
}
