# Validator Operations Runbook

## Clock Synchronization Guardrails

Validators must keep their host clocks disciplined to avoid timestamp drift. Blocks are
accepted only when their timestamps fall within a narrow window derived from the last
accepted block and the local wall clock. Set `BlockTimestampToleranceSeconds` in the
validator configuration (and governance policy) to the agreed tolerance—5 seconds by
default—and ensure every validator applies the same value. Nodes reject blocks that
arrive more than the configured tolerance ahead of the local clock or older than the
last accepted timestamp, so chrony/ntpd monitoring should alert on offsets approaching
half of the tolerance.

### Operational Checklist

- Monitor `chronyc tracking` or `timedatectl status` at least once per hour and alert if
  drift exceeds 2 seconds.
- Audit the deployed `config.toml` to confirm `BlockTimestampToleranceSeconds` matches
  the network governance policy before rotating validators.
- Investigate `block timestamp outside allowed window` errors immediately; they
  indicate either a validator clock skew or a faulty block producer replaying stale
  heights. Validate the last accepted timestamp from state before re-enabling signing.
