import express from 'express';
import morgan from 'morgan';
import type { Request, Response } from 'express';
import type { EscrowSession } from './types.js';
import { resolveConfig } from './config.js';
import { EscrowClient } from './escrowClient.js';
import { createWebhookVerifier, WebhookEvent } from './webhooks.js';

interface CreateSessionBody {
  orderId: string;
  customerWalletAddress?: string;
}

interface EscrowWebhookPayload {
  escrow_id: string;
  status: string;
  note?: string;
  amount?: {
    currency: string;
    value: string;
  };
}

const sessionsByOrder = new Map<string, EscrowSession>();
const sessionsBySessionId = new Map<string, EscrowSession>();
const sessionsByEscrowId = new Map<string, EscrowSession>();
const orderIdByEscrow = new Map<string, string>();
const orderIdBySession = new Map<string, string>();

const normaliseStatus = (status: string) => status.toUpperCase() as EscrowSession['status'];

function upsertSession(session: EscrowSession, orderId?: string) {
  const resolvedOrderId = orderId || orderIdByEscrow.get(session.escrowId) || orderIdBySession.get(session.sessionId);
  if (resolvedOrderId) {
    sessionsByOrder.set(resolvedOrderId, session);
    orderIdByEscrow.set(session.escrowId, resolvedOrderId);
    orderIdBySession.set(session.sessionId, resolvedOrderId);
  }
  sessionsBySessionId.set(session.sessionId, session);
  sessionsByEscrowId.set(session.escrowId, session);
}

function getSessionByEscrowId(escrowId: string): EscrowSession | undefined {
  return sessionsByEscrowId.get(escrowId);
}

async function bootstrap() {
  const config = await resolveConfig();
  const client = new EscrowClient(config);
  const verifyWebhook = createWebhookVerifier(config.webhookSecret);

  const app = express();
  app.use(morgan('dev'));

  app.get('/healthz', (_req, res) => {
    res.json({ ok: true });
  });

  app.post('/webhooks/escrow', express.raw({ type: 'application/json' }), (req: Request, res: Response) => {
    const signature = req.get('x-nhb-signature');
    const timestamp = req.get('x-nhb-timestamp');
    const verified = verifyWebhook(req.body as Buffer, signature, timestamp);
    if (!verified) {
      res.status(401).send('invalid signature');
      return;
    }

    let event: WebhookEvent<EscrowWebhookPayload>;
    try {
      event = JSON.parse((req.body as Buffer).toString('utf8'));
    } catch (err) {
      res.status(400).send('invalid json');
      return;
    }

    const payload = event.data;
    if (!payload?.escrow_id || !payload.status) {
      res.status(202).send('ignored');
      return;
    }

    const existing = getSessionByEscrowId(payload.escrow_id);
    if (existing) {
      const history = existing.history ? [...existing.history] : [];
      const status = normaliseStatus(payload.status);
      history.push({ status, at: event.created_at, note: payload.note });
      const updated: EscrowSession = {
        ...existing,
        status,
        history
      };
      upsertSession(updated);
      console.log('Webhook updated escrow session', updated.escrowId, updated.status);
    } else {
      console.warn('Received webhook for unknown escrow', payload.escrow_id);
    }

    res.status(200).send('ok');
  });

  app.use(express.json());

  app.post('/api/checkout/session', async (req: Request<unknown, unknown, CreateSessionBody>, res: Response) => {
    const { orderId, customerWalletAddress } = req.body || {};
    if (!orderId) {
      res.status(400).json({ message: 'orderId is required' });
      return;
    }

    const existing = sessionsByOrder.get(orderId);
    if (existing) {
      res.json(existing);
      return;
    }

    try {
      const session = await client.createCheckoutSession(orderId, customerWalletAddress);
      upsertSession(session, orderId);
      res.json(session);
    } catch (err) {
      console.error('Failed to create checkout session', err);
      res.status(502).json({ message: 'Unable to reach NHB API' });
    }
  });

  app.get('/api/checkout/session/:sessionId', async (req: Request<{ sessionId: string }>, res: Response) => {
    const { sessionId } = req.params;
    try {
      const session = await client.getCheckoutSession(sessionId);
      upsertSession(session);
      res.json(session);
    } catch (err) {
      console.error('Failed to fetch checkout session', err);
      res.status(502).json({ message: 'Unable to fetch escrow session' });
    }
  });

  app.post('/api/escrow/:escrowId/deliver', async (req: Request<{ escrowId: string }>, res: Response) => {
    const { escrowId } = req.params;
    try {
      const session = await client.markDelivered(escrowId);
      upsertSession(session);
      res.json(session);
    } catch (err) {
      console.error('Failed to mark delivery', err);
      res.status(502).json({ message: 'Unable to mark escrow delivery' });
    }
  });

  app.post('/api/escrow/:escrowId/release', async (req: Request<{ escrowId: string }>, res: Response) => {
    const { escrowId } = req.params;
    try {
      const session = await client.releaseEscrow(escrowId);
      upsertSession(session);
      res.json(session);
    } catch (err) {
      console.error('Failed to release escrow', err);
      res.status(502).json({ message: 'Unable to release escrow funds' });
    }
  });

  app.listen(config.port, () => {
    console.log(`Escrow merchant demo listening on :${config.port}`);
  });
}

bootstrap().catch((err) => {
  console.error('Failed to bootstrap merchant demo server', err);
  process.exit(1);
});
