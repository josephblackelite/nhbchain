export function formatNumber(value: number) {
  return value.toLocaleString('en-US', {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2
  });
}

export function clamp(value: number, min: number, max: number) {
  return Math.min(Math.max(value, min), max);
}
