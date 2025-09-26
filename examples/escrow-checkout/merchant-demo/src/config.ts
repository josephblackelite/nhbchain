import { config as loadEnv } from 'dotenv';
import { SecretsManagerClient, GetSecretValueCommand } from '@aws-sdk/client-secrets-manager';
import { SSMClient, GetParameterCommand } from '@aws-sdk/client-ssm';
import type { EscrowDemoConfig } from './types.js';

loadEnv();

interface SecretBundle {
  apiKey?: string;
  apiSecret?: string;
  webhookSecret?: string;
  walletPrivateKey?: string;
}

async function fetchSecretsFromSecretsManager(arn: string, region?: string): Promise<SecretBundle | undefined> {
  const client = new SecretsManagerClient({ region });
  const result = await client.send(new GetSecretValueCommand({ SecretId: arn }));
  if (!result.SecretString) return undefined;
  try {
    return JSON.parse(result.SecretString) as SecretBundle;
  } catch (err) {
    console.warn('Secrets Manager secret is not valid JSON, skipping');
    return undefined;
  }
}

async function fetchSecretsFromSsm(parameterName: string, region?: string): Promise<SecretBundle | undefined> {
  const client = new SSMClient({ region });
  const result = await client.send(new GetParameterCommand({ Name: parameterName, WithDecryption: true }));
  const value = result.Parameter?.Value;
  if (!value) return undefined;
  try {
    return JSON.parse(value) as SecretBundle;
  } catch (err) {
    console.warn('SSM parameter is not valid JSON, skipping');
    return undefined;
  }
}

function validateConfig(config: Partial<EscrowDemoConfig>): asserts config is EscrowDemoConfig {
  const missing = ['apiBase', 'apiKey', 'apiSecret', 'webhookSecret', 'walletPrivateKey', 'port'].filter(
    (key) => (config as Record<string, unknown>)[key] == null
  );
  if (missing.length > 0) {
    throw new Error(`Missing escrow merchant demo configuration values: ${missing.join(', ')}`);
  }
}

export async function resolveConfig(): Promise<EscrowDemoConfig> {
  const region = process.env.AWS_REGION || process.env.AWS_DEFAULT_REGION || 'us-east-1';
  const config: Partial<EscrowDemoConfig> = {
    apiBase: process.env.NHB_API_BASE || 'https://api.nhbcoin.net',
    apiKey: process.env.NHB_API_KEY,
    apiSecret: process.env.NHB_API_SECRET,
    webhookSecret: process.env.NHB_WEBHOOK_SECRET,
    walletPrivateKey: process.env.NHB_WALLET_SECRET,
    port: process.env.PORT ? parseInt(process.env.PORT, 10) : 4000
  };

  const secretsArn = process.env.ESCROW_SECRETS_ARN;
  if (secretsArn) {
    try {
      const bundle = await fetchSecretsFromSecretsManager(secretsArn, region);
      Object.assign(config, bundle);
    } catch (err) {
      console.warn('Unable to fetch secrets from Secrets Manager', err);
    }
  }

  const parameterName = process.env.ESCROW_SSM_PARAMETER;
  if (parameterName) {
    try {
      const bundle = await fetchSecretsFromSsm(parameterName, region);
      Object.assign(config, bundle);
    } catch (err) {
      console.warn('Unable to fetch secrets from SSM Parameter Store', err);
    }
  }

  validateConfig(config);
  return config;
}
