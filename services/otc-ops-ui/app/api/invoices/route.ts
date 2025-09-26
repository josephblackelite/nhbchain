import { NextResponse } from "next/server";
import { store } from "../../../data/invoices";
import { InvoiceFilters } from "../../../lib/types";

function parseFilters(url: URL): InvoiceFilters {
  const filters: InvoiceFilters = {};
  const stage = url.searchParams.get("stage");
  if (stage) filters.stage = stage as InvoiceFilters["stage"];
  const branch = url.searchParams.get("branch");
  if (branch) filters.branch = branch;
  const status = url.searchParams.get("status");
  if (status) filters.status = status as InvoiceFilters["status"];
  const minDate = url.searchParams.get("minDate");
  if (minDate) filters.minDate = minDate;
  const maxDate = url.searchParams.get("maxDate");
  if (maxDate) filters.maxDate = maxDate;
  const minAmount = url.searchParams.get("minAmount");
  if (minAmount) filters.minAmount = Number(minAmount);
  const maxAmount = url.searchParams.get("maxAmount");
  if (maxAmount) filters.maxAmount = Number(maxAmount);
  return filters;
}

export function GET(request: Request) {
  const url = new URL(request.url);
  const filters = parseFilters(url);
  const invoices = store.list(filters);
  return NextResponse.json({ invoices });
}
