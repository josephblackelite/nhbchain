import { NextRequest, NextResponse } from 'next/server';

import { bindEmailToAlias } from '../../../../../lib/identity-gateway';

export async function POST(request: NextRequest) {
  try {
    const payload = await request.json();
    const aliasId = typeof payload?.aliasId === 'string' ? payload.aliasId : '';
    const email = typeof payload?.email === 'string' ? payload.email : '';
    const consent = typeof payload?.consent === 'boolean' ? payload.consent : false;
    if (!aliasId.trim() || !email.trim()) {
      return NextResponse.json({ error: 'aliasId and email required' }, { status: 400 });
    }
    const idempotencyKey = request.headers.get('idempotency-key') ?? undefined;
    const response = await bindEmailToAlias(aliasId, email, consent, { idempotencyKey });
    return NextResponse.json(response);
  } catch (error) {
    console.error('identity/alias/bind-email failed', error);
    const status = typeof (error as { status?: number })?.status === 'number' ? (error as { status?: number }).status! : 502;
    const message =
      (error as { body?: string })?.body || (error instanceof Error ? error.message : 'upstream identity error');
    return NextResponse.json({ error: message }, { status });
  }
}
