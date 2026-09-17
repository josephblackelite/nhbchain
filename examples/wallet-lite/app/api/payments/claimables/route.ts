import { NextRequest, NextResponse } from 'next/server';
import { rpcRequest } from '../../../lib/rpc';
import { computeEmailHash } from '../../../lib/email';
import { normalizeAmount, formatDeadline } from '../../../lib/identity';

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

export async function POST(req: NextRequest) {
  const body = (await req.json().catch(() => ({}))) as ClaimableBody;
  const payer = body.payer?.trim();
  const token = body.token?.trim().toUpperCase() || 'NHB';
  const amountInput = body.amount?.trim() ?? '0';
  const deadlineHours = typeof body.deadlineHours === 'number' ? body.deadlineHours : 24;
  if (!payer) {
    return NextResponse.json({ error: 'payer is required' }, { status: 400 });
  }
  try {
    const amount = normalizeAmount(amountInput);
    const deadline = formatDeadline(deadlineHours);
    let recipient: string | undefined;
    switch (body.recipientType) {
      case 'email':
        if (!body.email) {
          throw new Error('email required');
        }
        recipient = computeEmailHash(body.email);
        break;
      case 'hash':
        if (!body.recipientHash) {
          throw new Error('recipient hash required');
        }
        recipient = body.recipientHash;
        break;
      default:
        if (!body.alias) {
          throw new Error('alias required');
        }
        recipient = body.alias;
        break;
    }
    const payload = {
      payer,
      recipient,
      token,
      amount,
      deadline
    };
    const result = await rpcRequest('identity_createClaimable', [payload], true);
    return NextResponse.json(result, { status: 200 });
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 400 });
  }
}
