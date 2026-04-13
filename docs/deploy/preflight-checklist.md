# Production Preflight Checklist

Complete these checks before relaunching NHBChain:

1. Rotate NOWPayments API and IPN secrets.
2. Regenerate genesis and clear old chain state intentionally.
3. Verify `config.toml` with `scripts/verify_prod_config.sh`.
4. Confirm `payments-gateway`, `payoutd`, `ops-reporting`, and OTC env files exist server-side.
5. Confirm payout policies and hold store paths are correct.
6. Confirm treasury hot wallet, cold wallet, and signer secrets resolve correctly.
7. Confirm NOWPayments webhook URL resolves over HTTPS.
8. Confirm bearer tokens and TLS materials are present for admin surfaces.
9. Confirm systemd service files are installed and enabled.
10. Run inbound mint validation end to end.
11. Run outbound USDT/USDC payout validation end to end.
12. Run OTC invoice -> approval -> sign-and-submit validation.
13. Confirm ops reporting surfaces show mint, payout, treasury, and merchant data.
14. Confirm monitoring and alerting are active for the core services.
