# Solar Trust Gate

> **Self-verifying Solar** — a governance layer that sits inside an Upstage Studio pipeline.
> Perception through statistics (Studio). Judgment through structure (Solar Trust Gate).

**Live demo:** [https://solar-trust-gate.fly.dev/](https://solar-trust-gate.fly.dev/)

---

## What it is

Solar Trust Gate is a runtime **judgment verification layer** built on top of Upstage Document AI and Solar Pro 3.

Studio's parse → classify → extract turns messy documents into high-fidelity structured data. Solar Trust Gate takes it further: every LLM judgment is independently audited against the source facts before it leaves the pipeline, producing evidence chains that are regulator-readable and structurally traceable.

Each agent action is a contract-bounded transformation:

```
F : A → B | P
```

**A** is the extracted-fact anchor (Upstage Document AI), **B** is the output schema, and **P** are pre/post-conditions derived from the domain contract (TFC).

---

## Pipeline

```
ups-parse → ups-classify → TCE → TFC ─┐
                         ↓             ├→ L1 → Solar-F → L2 → [judgment + evidence log]
                    ups-extract → A ───┘
L0 (boundary gate, pure-Go) — scans raw text first, before any LLM sees it
```

| Gate | Role | Runtime |
|---|---|---|
| **L0** — ingress gate | Pure-Go regex DPI + 5 escalators. Blocks injection, exfiltration, credential theft in <1 ms. | pure-Go (no LLM) |
| **L1** — pre-check | Lightweight validation of preconditions and completeness against TFC + A. | Solar Pro 3 |
| **F** — Solar judgment | Korean-language adjudication over anchor A → {verdict, rationale, evidence refs, approved amount}. | Solar Pro 3 |
| **L2** — epistemic audit | Traces F's every cited evidence ref back to anchor A. Tags each with ⊢ ⊨ ⊬ ⊥. Detects drift. | Solar Pro 3 |
| **L3** — egress gate | Output scrub before the response leaves the boundary. | pure-Go |

The worker (F) and the auditor (L2) are the same Solar model. The difference is not a handicap — it is structure: L2 receives an independent anchor A and a narrow audit task, not F's context.

---

## Demo — Korean health insurance claims

Three pre-loaded scenarios show the pipeline's behavior end-to-end.

| Scenario | Expected result |
|---|---|
| `claim-정상.pdf` — clean claim | L0 pass → L1 pass → F approves → L2 confirms all evidence refs present, risk_score low |
| `claim-함정-v3.pdf` — trap claim (forged rider basis) | **Two-act hero.** Base hospitalization (750,000 KRW) is within policy limit — F can approve it alone. Premium room surcharge (1,200,000 KRW) is a basic-policy exclusion; the claim narrative asserts CI-RIDER-2026-07 as the sole coverage basis. No rider certificate is attached. **verify=OFF:** F takes the rider claim at face value and approves the full 1,950,000 KRW, citing CI-RIDER in evidence refs. **verify=ON:** L2 traces F's rider citation back to anchor A — no 가입증명서 in attached_docs → ⊬ EXTRAPOLATED, TFC R5 violation, risk_score spikes. |
| `claim-악성.pdf` — malicious injection | L0 DENY in <1 ms (Korean-language exfil instruction detected). L1/L2/F never reached. |

The trap scenario is the core demonstration: Solar approves plausibly when verification is off, and catches its own fabricated evidence when verification is on — without swapping models or handicapping the worker.

---

## Run locally

```bash
git clone https://github.com/gem-squared/solar-trust-gate.git
cd solar-trust-gate

export UPSTAGE_API_KEY=your-key        # api.upstage.ai
export UPSTAGE_MODEL=solar-pro3-260323
export PORT=8090

go build -o solar-trust-gate ./console/
./solar-trust-gate
```

Open [http://localhost:8090/](http://localhost:8090/) — Korean insurance claim demo UI.

---

## Repo layout

```
solar-trust-gate/
├── console/                          # Go backend + embedded static UI
│   ├── main.go                       # HTTP routes
│   ├── claim_handlers.go             # POST /api/claim/process — pipeline entry point
│   ├── upstage_ingest.go             # Upstage Document Parse + Extract → anchor A
│   ├── solar_audit_gate.go           # L1 / L2 Solar self-verification gates
│   ├── lobstertrap.go                # L0/L3 boundary gate (pure-Go DPI + escalators)
│   ├── ce_registry.go                # CE spec loader
│   ├── audit_log.go                  # SQLite layer audit log
│   └── static/                       # solar-claim.html (demo UI) · workflow-canvas.html
├── console/demo-assets/korean-claims/   # 3 demo PDFs + synthesis scripts
├── .gem-squared/ce-registry/         # Korean insurance adjudication CE spec (6 pipeline stages)
├── policies/                         # L0/L3 policy YAML
├── Dockerfile · fly.toml             # Fly.io deploy
└── go.mod · go.sum                   # Go 1.25, pure-Go SQLite
```

---

## API endpoints

| Method · Path | Description |
|---|---|
| `GET /` | Korean insurance claim demo |
| `GET /canvas` | Pipeline architecture canvas |
| `POST /api/claim/process` | Upload PDF → full pipeline → judgment + gate result |
| `GET /api/claim/scenarios` | 3 pre-loaded demo scenarios with embedded PDF bytes |
| `GET /api/audit-log` | Query SQLite audit trail |

---

## Tech

- **Runtime:** Go 1.25, single binary, no external runtime dependencies
- **LLM:** Upstage Solar Pro 3 (`solar-pro3-260323`) — all three roles: L1, F, L2
- **Document AI:** Upstage Document Parse + Information Extract
- **Boundary gates:** pure-Go (L0/L3) — regex DPI, no LLM at ingress/egress
- **Persistence:** pure-Go SQLite (`mattn/go-sqlite3`) for audit log
- **Deploy:** Fly.io (`solar-trust-gate.fly.dev`)

---

## References

- `console/lobstertrap.go` · `console/lobstertrap_escalators.go` — L0/L3 gate implementation
- `console/upstage_ingest.go` — Document Parse + Extract integration
- `console/solar_audit_gate.go` — L1/L2 Solar gate and response parser
- `console/claim_handlers.go` — main pipeline orchestration (F + verification path)

---

Solar Trust Gate · [GEM².AI](https://gemsquared.ai) · david@gemsquared.ai · MIT License
