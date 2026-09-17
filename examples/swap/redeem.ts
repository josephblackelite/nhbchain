import fetch from "node-fetch";

type ThrottleResponse = {
  allowed: boolean;
};

type PolicyResponse = {
  id: string;
  mint_limit: number;
  redeem_limit: number;
  window_seconds: number;
};

async function redeemVoucher() {
  const policy = await fetch("http://localhost:7074/admin/policy").then((res) => res.json() as Promise<PolicyResponse>);
  console.log(`Current redeem window: ${policy.window_seconds}s, limit=${policy.redeem_limit}`);

  const throttle = await fetch("http://localhost:7074/admin/throttle/check", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ action: "redeem" }),
  }).then((res) => res.json() as Promise<ThrottleResponse>);

  if (!throttle.allowed) {
    throw new Error("redeem throttle exceeded");
  }

  // Submit redeem transaction to the public gateway here.
  console.log("Redeem slot reserved, submitting voucher...");
}

redeemVoucher().catch((err) => {
  console.error(err);
  process.exit(1);
});
