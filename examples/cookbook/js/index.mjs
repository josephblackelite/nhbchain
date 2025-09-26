import crypto from 'node:crypto';

const rpcUrl = (process.env.NHB_RPC_URL || 'https://rpc.nhbcoin.net').trim();
const apiBase = (process.env.NHB_API_BASE || 'https://api.nhbcoin.net/escrow/v1').trim().replace(/\/$/, '');
const address = (process.env.NHB_ADDRESS || '').trim();
const apiKey = (process.env.NHB_API_KEY || '').trim();
const apiSecret = (process.env.NHB_API_SECRET || '').trim();

if (!address) {
  console.error('NHB_ADDRESS environment variable is required');
  process.exit(1);
}

console.log(`RPC base: ${rpcUrl}`);
console.log(`REST base: ${apiBase}`);
console.log(`Address: ${address}`);
console.log('');

await runBalance();
await runLatestTransactions();

if (!apiKey || !apiSecret) {
  console.warn('Skipping REST escrow lookup (set NHB_API_KEY and NHB_API_SECRET to enable).');
  process.exit(0);
}

await runEscrowLookup();

async function runBalance() {
  console.log('==> nhb_getBalance');
  const result = await rpcCall('nhb_getBalance', [address]);
  console.log(JSON.stringify(result, null, 2));
  console.log('');
}

async function runLatestTransactions() {
  console.log('==> nhb_getLatestTransactions');
  const result = await rpcCall('nhb_getLatestTransactions', [10]);
  if (!Array.isArray(result) || result.length === 0) {
    console.log('no recent transactions returned');
  } else {
    result.forEach((tx, idx) => {
      console.log(`${String(idx + 1).padStart(2, ' ')}. ${tx.from} -> ${tx.to} (${tx.value})`);
    });
  }
  console.log('');
}

async function runEscrowLookup() {
  console.log('==> GET /trades (escrow gateway)');
  const params = new URLSearchParams({ buyer: address, status: 'SETTLED', limit: '5' });
  const { body, status } = await restRequest('GET', '/trades', params, undefined);
  if (status >= 400) {
    console.error(`gateway returned status ${status}: ${body}`);
    process.exit(1);
  }
  const parsed = JSON.parse(body);
  if (!Array.isArray(parsed?.data) || parsed.data.length === 0) {
    console.log('no settled trades for buyer; try seller or adjust filters');
  } else {
    parsed.data.forEach((trade) => {
      console.log(`trade ${trade.id} status ${trade.status} amount ${trade.amount}`);
    });
  }
  console.log('');
}

async function rpcCall(method, params = []) {
  const payload = {
    jsonrpc: '2.0',
    id: 1,
    method,
    params,
  };
  const response = await fetch(rpcUrl, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  if (!response.ok) {
    const text = await response.text();
    throw new Error(`rpc responded with ${response.status}: ${text}`);
  }
  const json = await response.json();
  if (json.error) {
    throw new Error(`rpc error ${json.error.code}: ${json.error.message}`);
  }
  if (json.result === undefined) {
    throw new Error('rpc result missing');
  }
  return json.result;
}

async function restRequest(method, path, params, body) {
  const url = new URL(apiBase + path);
  if (params && [...params.keys()].length > 0) {
    url.search = params.toString();
  }

  const timestamp = new Date().toISOString();
  const canonicalPath = url.pathname + (url.search || '');
  let payload = '';
  if (method !== 'GET') {
    payload = typeof body === 'string' ? body : JSON.stringify(body ?? {});
  }
  const stringToSign = [method.toUpperCase(), canonicalPath, payload, timestamp].join('\n');
  const signature = crypto.createHmac('sha256', apiSecret).update(stringToSign).digest('base64');

  const headers = {
    'X-API-Key': apiKey,
    'X-Timestamp': timestamp,
    'X-Signature': signature,
  };
  if (method !== 'GET') {
    headers['Content-Type'] = 'application/json';
  }

  const response = await fetch(url, {
    method,
    headers,
    body: method === 'GET' ? undefined : payload,
  });

  const text = await response.text();
  return { body: text, status: response.status };
}
