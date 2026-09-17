import { NextRequest, NextResponse } from 'next/server';

import { bindEmailToAlias } from '../../../../../lib/identity-gateway';

export async function POST(request: NextRequest) {
  try {
    const payload = await request.json();
    const aliasId = typeof payload?.aliasId === 'string' ? payload.aliasId : '';
    const email = typeof payload?.email === 'string' ? payload.email : '';
    const consent = typeof payload?.consent === 'boolean' ? payload.consent : false;
    const bindToken = typeof payload?.bindToken === 'string' ? payload.bindToken : '';
    if (!aliasId.trim() || !email.trim()) {
      return NextResponse.json({ error: 'aliasId and email required' }, { status: 400 });
    }
    // NHB-AUDIT-S6: require the caller to supply the token issued by their
    // own /identity/email/verify call rather than trusting aliasId/email
    // alone -- the gateway itself now enforces this too, but failing fast
    // here gives a clearer error than a 401 bounced back from upstream.
    if (!bindToken.trim()) {
      return NextResponse.json({ error: 'bindToken required' }, { status: 400 });
    }
    const idempotencyKey = request.headers.get('idempotency-key') ?? undefined;
    const response = await bindEmailToAlias(aliasId, email, consent, bindToken, { idempotencyKey });
    return NextResponse.json(response);
  } catch (error) {
    console.error('identity/alias/bind-email failed', error);
    const status = typeof (error as { status?: number })?.status === 'number' ? (error as { status?: number }).status! : 502;
    const message =
      (error as { body?: string })?.body || (error instanceof Error ? error.message : 'upstream identity error');
    return NextResponse.json({ error: message }, { status });
  }
}
