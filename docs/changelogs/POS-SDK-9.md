# POS-SDK-9: NHB-Pay spec + NFC/NDEF + SDK examples

* Published the NHB Pay URI specification with canonical signing rules, updated
  intent fields, and an end-to-end example URI.
* Documented the NFC NDEF layouts for NHB Pay, including the dual URI/CBOR
  records and the expected signature bytes.
* Added Go and TypeScript SDK examples that create, sign, submit, and watch POS
  intents via gRPC, demonstrating canonical string construction and realtime
  finality streaming.
* Documented the POS gateway HTTP endpoints for submitting intents and polling
  status, with sample requests, responses, and error schemas.
