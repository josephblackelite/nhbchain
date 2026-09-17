export function formatAmount(raw?: string, decimals = 18): string {
  if (!raw) return '0';
  const value = raw.trim();
  if (!/^[0-9]+$/.test(value)) {
    return raw;
  }
  if (decimals === 0) return value;
  if (value.length <= decimals) {
    const padded = value.padStart(decimals, '0');
    const whole = '0';
    const fraction = padded.replace(/0+$/, '');
    return fraction ? `${whole}.${fraction}` : '0';
  }
  const whole = value.slice(0, value.length - decimals);
  const fraction = value.slice(-decimals).replace(/0+$/, '');
  return fraction ? `${whole}.${fraction}` : whole;
}

export function formatTimestamp(unixSeconds?: number): string {
  if (!unixSeconds) return '—';
  return new Date(unixSeconds * 1000).toLocaleString();
}

export function formatStatus(status?: string): string {
  if (!status) return 'unknown';
  return status
    .toString()
    .replace(/_/g, ' ')
    .replace(/\b\w/g, (ch) => ch.toUpperCase());
}

export function toBaseUnits(input: string, decimals = 18): string {
  const trimmed = input.trim();
  if (!trimmed) {
    throw new Error('Amount is required');
  }
  if (!/^[0-9]+(\.[0-9]+)?$/.test(trimmed)) {
    throw new Error('Amount must be a decimal number');
  }
  if (decimals === 0) {
    if (trimmed.includes('.')) {
      throw new Error('Amount cannot include decimals');
    }
    return trimmed.replace(/^0+/, '') || '0';
  }
  const [wholePart, fractionPart = ''] = trimmed.split('.');
  const sanitizedWhole = wholePart.replace(/^0+/, '') || '0';
  const fraction = (fractionPart + '0'.repeat(decimals)).slice(0, decimals);
  const combined = `${sanitizedWhole}${fraction}`.replace(/^0+/, '');
  return combined || '0';
}
