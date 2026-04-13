import { NextRequest, NextResponse } from 'next/server';

const MAX_OCCURRENCES = 6;

type Cadence = 'weekly' | 'monthly';

interface SubscriptionBody {
  alias?: string;
  amount?: string;
  cadence?: Cadence;
  startDate?: string;
}

export async function POST(req: NextRequest) {
  const body = (await req.json().catch(() => ({}))) as SubscriptionBody;
  const alias = body.alias?.trim();
  const amount = body.amount?.trim();
  const cadence = (body.cadence || 'monthly') as Cadence;
  const startDate = body.startDate ? new Date(body.startDate) : new Date();

  if (!alias || !amount) {
    return NextResponse.json({ error: 'alias and amount are required' }, { status: 400 });
  }

  if (Number.isNaN(startDate.getTime())) {
    return NextResponse.json({ error: 'startDate is invalid' }, { status: 400 });
  }

  const intervalDays = cadence === 'weekly' ? 7 : 30;
  const occurrences: Array<{ dueAt: string; amount: string }> = [];
  const start = new Date(startDate.getTime());

  for (let i = 0; i < MAX_OCCURRENCES; i += 1) {
    const next = new Date(start.getTime());
    next.setDate(start.getDate() + i * intervalDays);
    occurrences.push({ dueAt: next.toISOString(), amount });
  }

  return NextResponse.json(
    {
      alias,
      amount,
      cadence,
      occurrences,
    },
    { status: 200 }
  );
}
