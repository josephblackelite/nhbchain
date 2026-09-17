import type { NextApiRequest, NextApiResponse } from 'next';

const RPC_URL = process.env.NHB_RPC_URL ?? 'http://localhost:8545';
const RPC_TOKEN = process.env.NHB_RPC_TOKEN ?? process.env.NEXT_PUBLIC_NHB_RPC_TOKEN ?? '';

// NHB-AUDIT-C5: this handler used to forward ANY client-specified method
// to the node with the server's own privileged bearer token attached, no
// allowlist, no visitor authentication -- turning this demo, if deployed
// as-is, into a fully open backdoor to every privileged RPC method the
// underlying node exposes, not just the ones this app actually uses.
// Only the exact methods this UI calls (see pages/index.tsx) are allowed
// through; everything else is rejected before ever reaching the node.
//
// All four are mutating (they move funds or publish content on behalf of
// a caller/fan/creator address supplied in the request body) and none of
// them are verified server-side against the actual visitor -- this demo
// has no session/login/signature-verification system at all. Deploying
// this beyond a local/trusted-network demo requires adding real
// per-visitor authentication (e.g. a wallet-signature challenge) before
// any of these calls is allowed, not just this allowlist.
const ALLOWED_METHODS = new Set([
  'creator_publish',
  'creator_tip',
  'creator_stake',
  'creator_payouts',
]);

type RPCPayload = {
  jsonrpc: '2.0';
  id: number;
  method: string;
  params: unknown[];
};

type ErrorBody = { error: string };

type HandlerResponse = RPCPayload & { result?: unknown; error?: unknown };

export default async function handler(
  req: NextApiRequest,
  res: NextApiResponse<HandlerResponse | ErrorBody>
) {
  if (req.method !== 'POST') {
    res.setHeader('Allow', ['POST']);
    res.status(405).json({ error: 'Method Not Allowed' });
    return;
  }

  const { method, params } = req.body ?? {};
  if (typeof method !== 'string') {
    res.status(400).json({ error: 'method is required' });
    return;
  }
  if (!ALLOWED_METHODS.has(method)) {
    res.status(403).json({ error: `method not allowed: ${method}` });
    return;
  }

  const payload: RPCPayload = {
    jsonrpc: '2.0',
    id: Date.now(),
    method,
    params: Array.isArray(params) ? params : [],
  };

  try {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' };
    if (RPC_TOKEN) {
      headers['Authorization'] = `Bearer ${RPC_TOKEN}`;
    }
    const response = await fetch(RPC_URL, {
      method: 'POST',
      headers,
      body: JSON.stringify(payload),
    });
    const body = (await response.json()) as HandlerResponse;
    res.status(response.status).json(body);
  } catch (error) {
    res.status(500).json({ error: (error as Error).message });
  }
}
