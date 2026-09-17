'use client';

import QRCode from 'qrcode.react';
import { formatAmount } from '../lib/format';

export interface PayIntentProps {
  title: string;
  intent: {
    to: string;
    token: string;
    amount: string;
    memo?: string;
  } | null;
  description?: string;
}

export function PayIntentCard({ title, intent, description }: PayIntentProps) {
  if (!intent) {
    return (
      <div className="intent-card">
        <h3>{title}</h3>
        <p className="muted">Intent unavailable yet.</p>
      </div>
    );
  }

  const params = new URLSearchParams({
    to: intent.to,
    token: intent.token,
    amount: formatAmount(intent.amount),
    memo: intent.memo ?? ''
  });
  const uri = `znhb://pay?${params.toString()}`;

  return (
    <div className="intent-card">
      <h3>{title}</h3>
      {description ? <p className="muted">{description}</p> : null}
      <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap', alignItems: 'center' }}>
        <QRCode value={uri} size={128} includeMargin bgColor="#0f172a" fgColor="#e2e8f0" />
        <dl>
          <dt>Pay to</dt>
          <dd>{intent.to}</dd>
          <dt>Token</dt>
          <dd>{intent.token}</dd>
          <dt>Amount</dt>
          <dd>{formatAmount(intent.amount)}</dd>
          {intent.memo ? (
            <>
              <dt>Memo</dt>
              <dd>{intent.memo}</dd>
            </>
          ) : null}
          <dt>URI</dt>
          <dd style={{ wordBreak: 'break-all' }}>{uri}</dd>
        </dl>
      </div>
    </div>
  );
}
