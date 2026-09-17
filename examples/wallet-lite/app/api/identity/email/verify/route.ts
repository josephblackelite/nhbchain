import { NextRequest, NextResponse } from 'next/server';

import { completeEmailVerification } from '../../../../../lib/identity-gateway';

export async function POST(request: NextRequest) {
  try {
    const payload = await request.json();
    const email = typeof payload?.email === 'string' ? payload.email : '';
    const code = typeof payload?.code === 'string' ? payload.code : '';
    if (!email.trim() || !code.trim()) {
      return NextResponse.json({ error: 'email and code required' }, { status: 400 });
    }
    const idempotencyKey = request.headers.get('idempotency-key') ?? undefined;
    const response = await completeEmailVerification(email, code, { idempotencyKey });
    return NextResponse.json(response);
  } catch (error) {
    console.error('identity/email/verify failed', error);
    const status = typeof (error as { status?: number })?.status === 'number' ? (error as { status?: number }).status! : 502;
    const message =
      (error as { body?: string })?.body || (error instanceof Error ? error.message : 'upstream identity error');
    return NextResponse.json({ error: message }, { status });
  }
}
