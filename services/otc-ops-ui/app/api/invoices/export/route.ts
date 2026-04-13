import { NextResponse } from "next/server";
import { store } from "../../../../data/invoices";
import { InvoiceFilters } from "../../../../lib/types";

function toCsv(data: Record<string, string | number | null>[]) {
  const header = Object.keys(data[0] ?? {});
  const rows = data.map((row) =>
    header
      .map((key) => {
        const value = row[key];
        if (value === null || value === undefined) return "";
        const text = String(value).replace(/"/g, '""');
        if (text.includes(",")) {
          return `"${text}"`;
        }
        return text;
      })
      .join(",")
  );
  return [header.join(","), ...rows].join("\n");
}

export function GET(request: Request) {
  const url = new URL(request.url);
  const filters: InvoiceFilters = {};
  url.searchParams.forEach((value, key) => {
    switch (key) {
      case "branch":
        filters.branch = value;
        break;
      case "status":
        filters.status = value as InvoiceFilters["status"];
        break;
      case "stage":
        filters.stage = value as InvoiceFilters["stage"];
        break;
      case "minDate":
        filters.minDate = value;
        break;
      case "maxDate":
        filters.maxDate = value;
        break;
      case "minAmount":
        filters.minAmount = Number(value);
        break;
      case "maxAmount":
        filters.maxAmount = Number(value);
        break;
      default:
        break;
    }
  });

  const invoices = store.list(filters);
  const csv = toCsv(
    invoices.map((invoice) => ({
      id: invoice.id,
      branch: invoice.branch,
      counterparty: invoice.counterparty,
      amount: invoice.amount,
      currency: invoice.currency,
      status: invoice.status,
      stage: invoice.stage,
      rate: invoice.rate,
      twapReference: invoice.twapReference,
      voucherId: invoice.voucherId,
      txHash: invoice.txHash ?? "",
      lastUpdated: invoice.updatedAt
    }))
  );

  return new NextResponse(csv, {
    headers: {
      "Content-Type": "text/csv",
      "Content-Disposition": "attachment; filename=otc-invoices.csv"
    }
  });
}
