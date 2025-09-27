import { ReactNode } from 'react';

type StatCardProps = {
  label: string;
  value: ReactNode;
  helper?: string;
};

export function StatCard({ label, value, helper }: StatCardProps) {
  return (
    <div className="info-card">
      <h3>{label}</h3>
      <div style={{ fontSize: '1.5rem', fontWeight: 700 }}>{value}</div>
      {helper && <p>{helper}</p>}
    </div>
  );
}
