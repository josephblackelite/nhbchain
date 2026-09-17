import nacl from 'tweetnacl';
import bs58 from 'bs58';

const encoder = new TextEncoder();

export class WalletSigner {
  private readonly secretKey: Uint8Array;
  private readonly publicKeyBytes: Uint8Array;

  constructor(privateKey: string) {
    const decoded = bs58.decode(privateKey);
    if (decoded.length === 32) {
      const keyPair = nacl.sign.keyPair.fromSeed(decoded);
      this.secretKey = keyPair.secretKey;
      this.publicKeyBytes = keyPair.publicKey;
    } else if (decoded.length === 64) {
      const keyPair = nacl.sign.keyPair.fromSecretKey(decoded);
      this.secretKey = keyPair.secretKey;
      this.publicKeyBytes = keyPair.publicKey;
    } else {
      throw new Error('Wallet secret must be 32 byte seed or 64 byte secret key encoded as base58');
    }
  }

  getPublicKey(): string {
    return bs58.encode(this.publicKeyBytes);
  }

  sign(message: string): string {
    const signature = nacl.sign.detached(encoder.encode(message), this.secretKey);
    return bs58.encode(signature);
  }
}
