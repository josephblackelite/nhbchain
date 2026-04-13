import type { NextApiRequest, NextApiResponse } from 'next';

const RPC_URL = process.env.NHB_RPC_URL ?? 'http://localhost:8545';
const RPC_TOKEN = process.env.NHB_RPC_TOKEN ?? process.env.NEXT_PUBLIC_NHB_RPC_TOKEN ?? '';

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
