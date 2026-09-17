import { NextRequest, NextResponse } from 'next/server';

import { startEmailVerification } from '../../../../../lib/identity-gateway';

export async function POST(request: NextRequest) {
  try {
    const payload = await request.json();
    const email = typeof payload?.email === 'string' ? payload.email : '';
    if (!email.trim()) {
      return NextResponse.json({ error: 'email required' }, { status: 400 });
    }
    const aliasHint = typeof payload?.aliasHint === 'string' ? payload.aliasHint : undefined;
    const idempotencyKey = request.headers.get('idempotency-key') ?? undefined;
    const response = await startEmailVerification(email, aliasHint, { idempotencyKey });
    return NextResponse.json(response);
  } catch (error) {
    console.error('identity/email/register failed', error);
    const status = typeof (error as { status?: number })?.status === 'number' ? (error as { status?: number }).status! : 502;
    const message =
      (error as { body?: string })?.body || (error instanceof Error ? error.message : 'upstream identity error');
    return NextResponse.json({ error: message }, { status });
  }
}
