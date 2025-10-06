"use client";

import { ChangeEvent } from "react";
import { FundingStatus, InvoiceFilters, InvoiceStatus } from "../lib/types";

const statuses: InvoiceStatus[] = [
  "receipt_pending",
  "receipt_verified",
  "under_review",
  "awaiting_approval",
  "approved",
  "funding_pending",
  "funding_confirmed",
  "rejected",
  "escalated",
  "signed",
  "submitted"
];

const fundingStatuses: FundingStatus[] = ["pending", "confirmed", "rejected"];

interface Props {
  filters: InvoiceFilters;
  onChange: (filters: Partial<InvoiceFilters>) => void;
}

export function InvoiceFiltersForm({ filters, onChange }: Props) {
  const handleChange = (event: ChangeEvent<HTMLInputElement | HTMLSelectElement>) => {
    const { name, value } = event.target;
    if (name === "minAmount" || name === "maxAmount") {
      onChange({ [name]: value ? Number(value) : undefined });
    } else if (!value) {
      onChange({ [name]: undefined });
    } else {
      onChange({ [name]: value });
    }
  };

  return (
    <section className="filters">
      <label>
        Branch
        <input
          name="branch"
          placeholder="All"
          value={filters.branch ?? ""}
          onChange={handleChange}
        />
      </label>
      <label>
        Status
        <select name="status" value={filters.status ?? ""} onChange={handleChange}>
          <option value="">All</option>
          {statuses.map((status) => (
            <option key={status} value={status}>
              {status.replace(/_/g, " ")}
            </option>
          ))}
        </select>
      </label>
      <label>
        Funding Status
        <select
          name="fundingStatus"
          value={filters.fundingStatus ?? ""}
          onChange={handleChange}
        >
          <option value="">All</option>
          {fundingStatuses.map((status) => (
            <option key={status} value={status}>
              {status}
            </option>
          ))}
        </select>
      </label>
      <label>
        Min Date
        <input
          type="date"
          name="minDate"
          value={filters.minDate ?? ""}
          onChange={handleChange}
        />
      </label>
      <label>
        Max Date
        <input
          type="date"
          name="maxDate"
          value={filters.maxDate ?? ""}
          onChange={handleChange}
        />
      </label>
      <label>
        Min Amount
        <input
          type="number"
          name="minAmount"
          value={filters.minAmount ?? ""}
          onChange={handleChange}
        />
      </label>
      <label>
        Max Amount
        <input
          type="number"
          name="maxAmount"
          value={filters.maxAmount ?? ""}
          onChange={handleChange}
        />
      </label>
    </section>
  );
}
