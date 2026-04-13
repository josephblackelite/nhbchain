import { z } from 'zod';

const serverConfigSchema = z.object({
  rpcUrl: z.string().url(),
  rpcToken: z.string().min(1),
  chainId: z.string().min(1),
  wsUrl: z.string().url().optional(),
  appBaseUrl: z.string().url().optional(),
  emailSalt: z.string().min(1),
  identityGatewayUrl: z.string().url(),
  identityGatewayKey: z.string().min(1),
  identityGatewaySecret: z.string().min(1)
});

const clientConfigSchema = z.object({
  appBaseUrl: z.string().optional(),
  chainId: z.string().optional()
});

export type ServerConfig = z.infer<typeof serverConfigSchema>;
export type ClientConfig = z.infer<typeof clientConfigSchema>;

export function readServerConfig(): ServerConfig {
  const parsed = serverConfigSchema.safeParse({
    rpcUrl: process.env.NHB_RPC_URL,
    rpcToken: process.env.NHB_RPC_TOKEN,
    chainId: process.env.NHB_CHAIN_ID,
    wsUrl: process.env.NHB_WS_URL,
    appBaseUrl: process.env.APP_PUBLIC_BASE,
    emailSalt: process.env.IDENTITY_EMAIL_SALT,
    identityGatewayUrl: process.env.IDENTITY_GATEWAY_URL,
    identityGatewayKey: process.env.IDENTITY_GATEWAY_KEY,
    identityGatewaySecret: process.env.IDENTITY_GATEWAY_SECRET
  });
  if (!parsed.success) {
    const issues = parsed.error.issues.map((issue) => `${issue.path.join('.') || 'unknown'}: ${issue.message}`).join(', ');
    throw new Error(`Wallet Lite configuration invalid: ${issues}`);
  }
  return parsed.data;
}

export function readClientConfig(): ClientConfig {
  const parsed = clientConfigSchema.safeParse({
    appBaseUrl: process.env.NEXT_PUBLIC_APP_PUBLIC_BASE ?? process.env.APP_PUBLIC_BASE,
    chainId: process.env.NEXT_PUBLIC_NHB_CHAIN_ID ?? process.env.NHB_CHAIN_ID
  });
  if (!parsed.success) {
    throw new Error('Invalid client configuration');
  }
  return parsed.data;
}
