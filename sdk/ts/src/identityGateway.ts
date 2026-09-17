import crypto from 'crypto';

export interface IdentityGatewayClientConfig {
  baseUrl: string;
  apiKey: string;
  apiSecret: string;
  fetchImpl?: typeof fetch;
  clock?: () => number;
}

export interface RegisterEmailResponse {
  status: string;
  expiresIn: number;
}

export interface VerifyEmailResponse {
  status: string;
  verifiedAt: string;
  emailHash: string;
}

export interface BindEmailResponse {
  status: string;
  aliasId: string;
  emailHash: string;
  publicLookup: boolean;
}

export interface RequestOptions {
  idempotencyKey?: string;
}

export default class IdentityGatewayClient {
  private readonly baseUrl: URL;
  private readonly apiKey: string;
  private readonly apiSecret: string;
  private readonly fetchImpl: typeof fetch;
  private readonly clock: () => number;

  constructor(config: IdentityGatewayClientConfig) {
    if (!config.baseUrl?.trim()) {
      throw new Error('baseUrl required');
    }
    this.baseUrl = new URL(config.baseUrl);
    if (!config.apiKey?.trim()) {
      throw new Error('apiKey required');
    }
    if (!config.apiSecret?.trim()) {
      throw new Error('apiSecret required');
    }
    this.apiKey = config.apiKey.trim();
    this.apiSecret = config.apiSecret.trim();
    this.fetchImpl = config.fetchImpl ?? fetch;
    this.clock = config.clock ?? (() => Date.now());
  }

  async registerEmail(email: string, aliasHint?: string, options: RequestOptions = {}): Promise<RegisterEmailResponse> {
    const payload: Record<string, unknown> = { email };
    if (aliasHint && aliasHint.trim()) {
      payload.aliasHint = aliasHint;
    }
    return this.post<RegisterEmailResponse>('/identity/email/register', payload, options);
  }

  async verifyEmail(email: string, code: string, options: RequestOptions = {}): Promise<VerifyEmailResponse> {
    return this.post<VerifyEmailResponse>('/identity/email/verify', { email, code }, options);
  }

  async bindEmail(aliasId: string, email: string, consent: boolean, options: RequestOptions = {}): Promise<BindEmailResponse> {
    return this.post<BindEmailResponse>(
      '/identity/alias/bind-email',
      { aliasId, email, consent },
      options
    );
  }

  private async post<T>(path: string, body: unknown, options: RequestOptions): Promise<T> {
    const payload = JSON.stringify(body ?? {});
    const timestamp = Math.floor(this.clock() / 1000).toString();
    const bodyHash = crypto.createHash('sha256').update(payload).digest('hex');
    const message = `POST\n${path}\n${bodyHash}\n${timestamp}`;
    const signature = crypto.createHmac('sha256', this.apiSecret).update(message).digest('hex');
    const requestUrl = new URL(path, this.baseUrl);
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      'X-API-Key': this.apiKey,
      'X-API-Timestamp': timestamp,
      'X-API-Signature': signature
    };
    if (options.idempotencyKey?.trim()) {
      headers['Idempotency-Key'] = options.idempotencyKey.trim();
    }
    const response = await this.fetchImpl(requestUrl.toString(), {
      method: 'POST',
      headers,
      body: payload
    });
    const text = await response.text();
    if (!response.ok) {
      throw Object.assign(new Error(`identity gateway ${response.status}: ${text}`), {
        status: response.status,
        body: text
      });
    }
    return JSON.parse(text) as T;
  }
}
