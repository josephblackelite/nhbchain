import { NextRequest, NextResponse } from 'next/server';
import { rpcRequest } from '../../../lib/rpc';
import { deriveAliasId } from '../../../lib/identity';

interface ClaimBody {
  claimId?: string;
  payee?: string;
  preimage?: string;
  alias?: string;
}

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
    const payload = {
      claimId,
      payee,
      preimage
    };
    const result = await rpcRequest('identity_claim', [payload], true);
    return NextResponse.json(result, { status: 200 });
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 400 });
  }
}
