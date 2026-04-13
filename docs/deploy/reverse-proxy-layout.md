# Reverse Proxy Layout

Recommended public routing for the NHBChain production stack:

* `chain.nhbcoin.com`
  * node RPC and public chain APIs
* `pay.nhbcoin.com`
  * `payments-gateway`
  * NOWPayments inbound webhook endpoint
* `ops.nhbcoin.com`
  * `ops-reporting`
  * restricted admin/operator access only
* `otc.nhbcoin.com`
  * OTC gateway and OTC operator surfaces

Recommended internal ports on the chain EC2:

* node RPC: `127.0.0.1:8545`
* `payments-gateway`: `127.0.0.1:8084`
* `payoutd`: `127.0.0.1:7082`
* `ops-reporting`: `127.0.0.1:8091`
* `otc-gateway`: `127.0.0.1:8086`

Guidelines:

* terminate TLS at Nginx or Caddy
* keep service listeners private where possible
* expose only the endpoints needed publicly
* protect `ops-reporting` and `payoutd` admin surfaces behind auth plus network restrictions
* point NOWPayments IPN/webhooks at the payment hostname, not the raw node hostname
