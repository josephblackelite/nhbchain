import { NextRequest, NextResponse } from 'next/server';
import { normalizeAmount } from '../../../lib/identity';

interface ClaimableBody {
  payer?: string;
  amount?: string;
  token?: string;
  deadlineHours?: number;
  recipientType?: 'alias' | 'email' | 'hash';
  alias?: string;
  email?: string;
  recipientHash?: string;
}

// identity_createClaimable is disabled server-side (410 Gone): it used to
// mutate validator-local state outside the block pipeline, which guarantees
// a consensus fork/halt on this chain's 2-validator zero-quorum-slack
// topology, and no signed-transaction replacement exists yet (see
// rpc/identity_handlers.go's identityRPCDisabledMessage). This route returns
// the retired response itself, before ever calling the chain, so the
// failure is instant and self-explanatory instead of a generic upstream
// error.
const RETIRED_MESSAGE =
  'identity_createClaimable is disabled -- it mutated validator-local state outside the block pipeline, guaranteeing a consensus fork/halt on a 2-validator zero-quorum-slack chain; a signed-transaction replacement is pending.';

export async function POST(req: NextRequest) {
  const body = (await req.json().catch(() => ({}))) as ClaimableBody;
  const payer = body.payer?.trim();
  const amountInput = body.amount?.trim() ?? '0';
  if (!payer) {
    return NextResponse.json({ error: 'payer is required' }, { status: 400 });
  }
  try {
    // Malformed amounts and missing recipients are still rejected as 400.
    normalizeAmount(amountInput);
    switch (body.recipientType) {
      case 'email':
        if (!body.email?.trim()) {
          throw new Error('email required');
        }
        break;
      case 'hash':
        if (!body.recipientHash) {
          throw new Error('recipient hash required');
        }
        break;
      default:
        if (!body.alias) {
          throw new Error('alias required');
        }
        break;
    }
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 400 });
  }
  return NextResponse.json(
    { error: RETIRED_MESSAGE, retired: true, method: 'identity_createClaimable' },
    { status: 410 },
  );
}
