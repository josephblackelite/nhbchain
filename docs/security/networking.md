# Network Security Playbook

This guide documents the controls and monitoring required to protect nhbchain's networking stack.

## Perimeter controls

- **Ingress filtering.** Allow RPC, gRPC, and P2P ports only from trusted CIDR ranges. Deny all other inbound traffic by default.
- **DDoS mitigation.** Front public RPC endpoints with rate limiting (Cloudflare, Envoy) and enable SYN flood protection at the load balancer.
- **TLS enforcement.** Use mutual TLS for validator-to-validator links and TLS 1.2+ with modern ciphers for public APIs.

## Internal segmentation

- Place validators, gateways, and data stores in separate network segments. Use firewall rules to restrict lateral movement.
- Require jump hosts or VPN for administrative access, with MFA and hardware-backed SSH keys.
- Monitor East-West traffic for unusual spikes or protocol usage.

## Monitoring

- Collect NetFlow or VPC flow logs and store them for at least 90 days.
- Configure IDS/IPS (Zeek, Suricata) on validator segments. Alert on protocol violations, known malware signatures, or unexpected ports.
- Instrument Prometheus with connection count, handshake failure, and TLS renegotiation metrics.

## Incident response

1. Isolate affected nodes by updating security groups or firewall rules.
2. Rotate validator keys if compromise is suspected.
3. Replay flow logs to determine ingress origin and attack duration.
4. File an incident report and coordinate with the governance body for any on-chain mitigations.

## Maintenance

- Review firewall rules quarterly.
- Patch network appliances and load balancers within 14 days of a critical CVE.
- Test failover paths and firewall backups annually.
