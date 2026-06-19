/* ─────────────────────────────────────────────────────────────────── *
 * workflow-canvas.js — production canvas glue (WP-AO-38 Units 4-6)
 *
 * Unit 4 (this file, initial): CE-registry fetch + palette + drag-drop.
 * Unit 5 (added later):        edge type-check + save/load + dropdown.
 * Unit 6 (added later):        Run + SSE trace + execution-state animation.
 * ─────────────────────────────────────────────────────────────────── */

(function () {
  'use strict';

  // ── Module state ─────────────────────────────────────────────────
  const authKey = window.WF_AUTH_KEY || '';
  if (!authKey) {
    console.error('[wf-canvas] no auth key — should have been redirected by inline gate');
    return;
  }

  const $ = (sel) => document.querySelector(sel);

  /** Map: Drawflow integer node id (as JS number) → CE registry entry */
  const nodeCEMeta = new Map();
  /** CE registry payload (raw list of light entries from /api/crafter/ce-registry) */
  let ceRegistry = [];

  let editor = null;

  // ── WP-AO-41: Health-Insurance-Claim demo scenarios ──────────────
  // Hardcoded pre-fill inputs for the 5 demo scenarios. Pre-bundled so
  // judges click Run without typing JSON. Source: claim_A###_full_pipeline.json
  // stage_1_intake._input from the tpmn-contracts repo.
  const SCENARIOS = [
    {"name":"A001","label":"A001 — Hospitalisation (Pneumonia, 4 days), COMP-HEALTH-GOLD, Self, FULLY APPROVED","outcome":"Disbursed via direct_credit. All 6 pipeline stages pass.","input":{"policy_no":"HIC-2024-00123","policy_holder":"Tan Wei Ming","claimant_name":"Tan Wei Ming","claimant_relationship":"self","id_document_type":"nric","id_document_no":"S7801234A","date_of_birth":"1978-03-22","claim_date":"2025-04-14","incident_date":"2025-04-10","claim_type":"hospitalisation","provider_name":"Singapore General Hospital","provider_registration":"MOH-HOSP-00142","claim_amount_requested":18500.0,"supporting_documents":["medical_bill","discharge_summary","pre_auth_approval"],"claimant_contact_email":"tanweiming@email.sg","claimant_contact_phone":"+6591234567","medical_details":{"primary_diagnosis_icd10":"J18.9","procedure_cpt_codes":["99223"],"attending_physician":"Dr. Lim Wei Ming","physician_license_no":"MCR-12345A","pre_authorisation_no":"PA-2026-000042"},"payment_details":{"payment_mode":"direct_credit","bank_name":"DBS Bank","bank_account_no":"1234567890","bank_branch_code":"020","payee_name":"Tan Wei Ming"}}},
    {"name":"A002","label":"A002 — Outpatient (Hypertension), SILVER, Spouse Dependent, APPROVED w/ cost-sharing","outcome":"Disbursed via direct_credit. Spouse dependent verified. Cost-sharing applied.","input":{"policy_no":"HIC-2023-00456","policy_holder":"Lim Jia Hui","claimant_name":"Lim Kai Xuan","claimant_relationship":"spouse","id_document_type":"nric","id_document_no":"S8867890C","date_of_birth":"1988-11-30","claim_date":"2025-05-14","incident_date":"2025-05-14","claim_type":"outpatient","provider_name":"HealthFirst Medical Clinic","provider_registration":"MOH-CLIN-00887","claim_amount_requested":380.0,"supporting_documents":["medical_bill","prescription"],"claimant_contact_email":"limkaixuan@email.sg","claimant_contact_phone":"+6598765432","medical_details":{"primary_diagnosis_icd10":"I10","procedure_cpt_codes":["99213"],"attending_physician":"Dr. Sarah Chen","physician_license_no":"MCR-67890B","pre_authorisation_no":null},"payment_details":{"payment_mode":"direct_credit","bank_name":"OCBC","bank_account_no":"5550001234","bank_branch_code":"033","payee_name":"Lim Kai Xuan"}}},
    {"name":"A003","label":"A003 — Maternity, GOLD, Self, REJECTED at Stage 3 (waiting period)","outcome":"Pipeline halts at eligibility-check. Policy only 65 days old; maternity waiting period is 270 days.","input":{"policy_no":"HIC-2025-00789","policy_holder":"Priya Subramaniam","claimant_name":"Priya Subramaniam","claimant_relationship":"self","id_document_type":"nric","id_document_no":"S9023456D","date_of_birth":"1990-05-18","claim_date":"2025-05-14","incident_date":"2025-05-13","claim_type":"maternity","provider_name":"KK Women's and Children's Hospital","provider_registration":"MOH-HOSP-00273","claim_amount_requested":8200.0,"supporting_documents":["medical_bill","discharge_summary"],"claimant_contact_email":"priya.sub@email.sg","claimant_contact_phone":"+6583456789","medical_details":{"primary_diagnosis_icd10":"O80","procedure_cpt_codes":["59400"],"attending_physician":"Dr. Rajesh Kumar","physician_license_no":"MCR-11111C","pre_authorisation_no":"PA-2026-000099"},"payment_details":{"payment_mode":"direct_credit","bank_name":"UOB","bank_account_no":"7770005678","bank_branch_code":"016","payee_name":"Priya Subramaniam"}}},
    {"name":"A004","label":"A004 — Surgical, BRONZE, Self, Non-Panel Hospital, APPROVED at 60%","outcome":"Disbursed via provider_direct. Non-panel surcharge applied; net payable significantly reduced.","input":{"policy_no":"HIC-2022-00321","policy_holder":"Ahmad Zulkifli bin Hassan","claimant_name":"Ahmad Zulkifli bin Hassan","claimant_relationship":"self","id_document_type":"nric","id_document_no":"S7534567E","date_of_birth":"1975-09-03","claim_date":"2025-05-14","incident_date":"2025-05-02","claim_type":"surgical","provider_name":"Novena Surgical and Orthopaedic Centre","provider_registration":"PRV-HOSP-09921","claim_amount_requested":11500.0,"supporting_documents":["medical_bill","discharge_summary","pre_auth_approval"],"claimant_contact_email":"ahmad.hassan@email.sg","claimant_contact_phone":"+6597654321","medical_details":{"primary_diagnosis_icd10":"M54.5","procedure_cpt_codes":["27447"],"attending_physician":"Dr. Sarah Chen","physician_license_no":"MCR-67890B","pre_authorisation_no":"PA-2026-000200"},"payment_details":{"payment_mode":"provider_direct","bank_name":null,"bank_account_no":null,"bank_branch_code":null,"payee_name":"Novena Surgical and Orthopaedic Centre"}}},
    {"name":"A005","label":"A005 — Emergency, GOLD, Self, REJECTED at Stage 2 (duplicate claim)","outcome":"Pipeline halts at policy-verification. A paid claim for the same policy_no + incident_date + claim_type already exists.","input":{"policy_no":"HIC-2024-00099","policy_holder":"Chen Mei Ling","claimant_name":"Chen Mei Ling","claimant_relationship":"self","id_document_type":"nric","id_document_no":"S9145678F","date_of_birth":"1991-12-07","claim_date":"2025-05-14","incident_date":"2025-04-21","claim_type":"emergency","provider_name":"Tan Tock Seng Hospital","provider_registration":"MOH-HOSP-00391","claim_amount_requested":1850.0,"supporting_documents":["medical_bill","imaging_report"],"claimant_contact_email":"chenmei@email.sg","claimant_contact_phone":"+6587654321","medical_details":{"primary_diagnosis_icd10":"R51","procedure_cpt_codes":["99213"],"attending_physician":"Dr. Lim Wei Ming","physician_license_no":"MCR-12345A","pre_authorisation_no":null},"payment_details":{"payment_mode":"direct_credit","bank_name":"DBS Bank","bank_account_no":"9990001111","bank_branch_code":"020","payee_name":"Chen Mei Ling"}}}
  ];
  // ── WP-AO-60 hotfix — date-relative scenario rewrite ────────────
  // Hardcoded scenario dates (claim_date=2025-04-14 etc.) cause every run
  // to fail because the contract F-block requires `claim_date == today's
  // server-side date`. We rewrite each scenario's date fields at runtime:
  //   claim_date    → today (UTC+8)
  //   incident_date → today − (original_delta_days)
  // date_of_birth is left as authored (real birthdate).
  function ymdUTC8(d) {
    // Format YYYY-MM-DD in Singapore/Beijing UTC+8.
    const utc8 = new Date(d.getTime() + 8 * 3600 * 1000);
    return utc8.toISOString().slice(0, 10);
  }
  function dateRelativize(input) {
    if (!input || typeof input !== 'object') return input;
    const out = { ...input };
    const today = new Date();
    const todayStr = ymdUTC8(today);
    let delta = 4;
    if (input.claim_date && input.incident_date) {
      const cd = new Date(input.claim_date + 'T00:00:00Z');
      const id = new Date(input.incident_date + 'T00:00:00Z');
      const d = Math.round((cd.getTime() - id.getTime()) / 86400000);
      if (Number.isFinite(d) && d >= 0 && d <= 365) delta = d;
    }
    out.claim_date = todayStr;
    const incident = new Date(today.getTime() - delta * 86400 * 1000);
    out.incident_date = ymdUTC8(incident);
    return out;
  }

  /** Currently-selected scenario index in SCENARIOS, -1 = no pre-fill. */
  let selectedScenarioIdx = 0;

  function getSelectedScenarioInput() {
    if (selectedScenarioIdx < 0 || selectedScenarioIdx >= SCENARIOS.length) return null;
    // WP-AO-60 hotfix — rewrite hardcoded scenario dates to "today/today-N"
    // so the F-block's `claim_date == today` check passes regardless of
    // when the demo runs. Original deltas (claim_date − incident_date) are
    // preserved per scenario (A001=4, A002=0, A003=1, A004=12, A005=23).
    return dateRelativize(SCENARIOS[selectedScenarioIdx].input);
  }

  function populateScenarioPicker() {
    const sel = document.getElementById('wf-scenario-picker');
    if (!sel) return;
    sel.innerHTML = SCENARIOS.map((s, i) =>
      `<option value="${i}">${escapeHTML(s.label)}</option>`
    ).join('');
    sel.style.display = '';
    sel.addEventListener('change', (e) => {
      selectedScenarioIdx = parseInt(e.target.value, 10);
      const s = SCENARIOS[selectedScenarioIdx];
      if (s) toast(`Scenario ${s.name} pre-filled · ${s.outcome.slice(0, 80)}…`, 'ok', 3200);
    });
  }

  // ── WP-AO-41: auto-load the claims-demo workflow on first boot ───
  async function tryAutoLoadDemoWorkflow() {
    // Only auto-load if the canvas is empty (no nodes yet).
    if (editor && Object.keys(editor.export()?.drawflow?.Home?.data || {}).length > 0) return;
    try {
      // WP-AO-60 hotfix — pre-check via /api/workflow/list so we don't
      // log a noisy 404 in the console on fresh installs that don't have
      // the claims-demo workflow yet.
      const listRes = await fetch('/api/workflow/list', {
        headers: { 'X-Access-Key': authKey },
      });
      if (!listRes.ok) return;
      const listing = await listRes.json();
      const slugs = (listing.workflows || []).map((w) => w.slug || w);
      if (!slugs.includes('claims-demo')) return; // not deployed, skip silently

      const res = await fetch('/api/workflow/load?slug=claims-demo', {
        headers: { 'X-Access-Key': authKey },
      });
      if (!res.ok) return; // No demo on server — graceful no-op.
      const wf = await res.json();
      // Re-use the existing load path so nodeCEMeta is populated correctly.
      // Inline-equivalent of doLoad('claims-demo') minus the picker dropdown.
      editor.clearModuleSelected();
      const drawflowJSON = canonicalToDrawflow(wf);
      editor.import(drawflowJSON);
      nodeCEMeta.clear();
      for (const node of wf.nodes) {
        const id = parseInt(node.id.replace(/^n/, ''), 10);
        const meta = ceRegistry.find(
          (c) => `${c.workflow_slug}/${c.stage_slug}` === node.ce_slug
        );
        if (meta) nodeCEMeta.set(id, meta);
      }
      const t = $('#wf-title-input'); if (t) t.value = wf.title || '';
      const s = $('#wf-slug-input');  if (s) s.value = wf.workflow_slug || 'claims-demo';
      // WP-AO-50: restore audit-toggle state on auto-load too
      // WP-AO-52 U3: mirror runner nil→true default. Older saves omit
      // audit_l{1,2}; `!!undefined` would silently uncheck them.
      const l1T = document.getElementById('wf-audit-l1-toggle');
      const l2T = document.getElementById('wf-audit-l2-toggle');
      if (l1T) l1T.checked = wf.audit_l1 === undefined ? true : !!wf.audit_l1;
      if (l2T) l2T.checked = wf.audit_l2 === undefined ? true : !!wf.audit_l2;
      toast(`Pre-loaded "${wf.workflow_slug}" — ${wf.nodes.length} CEs ready`, 'ok', 2500);
    } catch (e) {
      console.warn('[wf-canvas] auto-load demo failed:', e);
    }
  }

  // ── Drawflow init ────────────────────────────────────────────────
  function initEditor() {
    const container = document.getElementById('drawflow');
    if (!container) {
      console.error('[wf-canvas] #drawflow container missing');
      return;
    }
    editor = new Drawflow(container);
    editor.reroute = true;
    editor.reroute_fix_curvature = true;
    editor.start();

    // Wire Drawflow lifecycle hooks
    editor.on('nodeRemoved', (id) => {
      nodeCEMeta.delete(Number(id));
      // Refresh badges (some edges may now be invalid)
      bridgeRecomputeAll();
    });
    editor.on('connectionCreated', (info) => {
      onConnectionCreated(info);
      // Compute + render badge for the new edge
      bridgeRecompute(Number(info.output_id), Number(info.input_id))
        .then(() => renderAllBridgeBadges());
    });
    editor.on('connectionRemoved', (info) => {
      bridgeState.delete(bridgeKey(Number(info.output_id), Number(info.input_id)));
      renderAllBridgeBadges();
    });
    editor.on('nodeCreated', () => {
      // newly dropped node — fetch its spec so future edges can resolve
      // (no immediate badge work since edges aren't drawn yet)
    });
    editor.on('translate', () => renderAllBridgeBadges());
    editor.on('zoom',      () => renderAllBridgeBadges());

    // WP-AO-56 hotfix — click on a connection line enters delete-mode
    // (red ✕ appears at midpoint; second click on ✕ deletes). Click
    // elsewhere clears delete-mode. NB: no stopPropagation — Drawflow
    // needs its own click handlers to keep connection rendering in sync.
    const dfRoot = document.getElementById('drawflow');
    dfRoot.addEventListener('click', (e) => {
      // Badge clicks have their own handler (delete confirm)
      if (e.target.closest('.wf-bridge-badge')) return;
      const path = e.target.closest('.connection .main-path');
      if (path) {
        const conn = path.closest('.connection');
        const ids = parseConnectionEndpoints(conn);
        if (!ids) return;
        const badge = conn.querySelector('.wf-bridge-badge');
        if (badge) enterDeleteMode(conn, badge);
        return;
      }
      // Click on canvas / node / anywhere else → clear delete-mode
      clearAllDeleteModes();
    });
    // Right-click on the line is a shortcut for immediate delete.
    dfRoot.addEventListener('contextmenu', (e) => {
      const path = e.target.closest('.connection .main-path');
      const badge = e.target.closest('.wf-bridge-badge');
      const conn = (badge ? badge.parentElement : (path ? path.closest('.connection') : null));
      if (!conn) return;
      e.preventDefault();
      const ids = parseConnectionEndpoints(conn);
      if (!ids) return;
      tryRemoveConnection(ids);
    });
  }

  // ── Edge type-compatibility check (Unit 5) ────────────────────────
  /** Cache: ce_slug → full CESpec (incl A, B, PPre, PPost). */
  const ceContractCache = new Map();

  async function fetchContract(slug) {
    if (ceContractCache.has(slug)) return ceContractCache.get(slug);
    try {
      const res = await fetch(
        '/api/workflow/ce-contract?slug=' + encodeURIComponent(slug),
        { headers: { 'X-Access-Key': authKey } }
      );
      if (!res.ok) return null;
      const spec = await res.json();
      ceContractCache.set(slug, spec);
      return spec;
    } catch (e) {
      console.error('[wf-canvas] contract fetch:', e);
      return null;
    }
  }

  /**
   * Compare source CE's B schema with target CE's A schema.
   * Loose match: if both are objects with field keys, target's A keys must
   * be a subset of source's B keys. If schemas are markdown or strings,
   * fall back to "warn but allow" — we can't structurally compare prose.
   * Returns { ok: boolean, reason: string }.
   */
  function compareSchemas(srcB, dstA) {
    const srcKeys = schemaKeys(srcB);
    const dstKeys = schemaKeys(dstA);
    if (srcKeys === null || dstKeys === null) {
      return { ok: true, reason: 'loose-match (prose schemas)' };
    }
    const missing = dstKeys.filter((k) => !srcKeys.includes(k));
    if (missing.length === 0) {
      return { ok: true, reason: `${dstKeys.length}/${srcKeys.length} fields satisfied` };
    }
    return { ok: false, reason: `missing in source B: ${missing.join(', ')}` };
  }

  function schemaKeys(schema) {
    if (!schema) return null;
    if (Array.isArray(schema)) return null;
    if (typeof schema === 'object') return Object.keys(schema);
    if (typeof schema !== 'string') return null;
    // Parse YAML-style schema text. WP-AO-66 fix: only capture TOP-LEVEL
    // keys (no leading whitespace) so nested fields like
    //   medical_details:
    //     primary_diagnosis_icd10: string
    //     procedure_cpt_codes: string[]
    // produce one key (`medical_details`), not three. The type segment is
    // optional — a bare 'medical_details:' line (whose value is on
    // subsequent indented lines) still counts as a top-level field.
    const out = [];
    const seen = new Set();
    const re = /^([a-zA-Z_][a-zA-Z0-9_]*)\s*:(?:\s*([a-zA-Z\[\]\?]+))?/gm;
    let m;
    while ((m = re.exec(schema)) !== null) {
      const k = m[1];
      if (!seen.has(k)) {
        seen.add(k);
        out.push(k);
      }
    }
    return out.length > 0 ? out : null;
  }

  // WP-AO-56 — bridge-badge state per edge.
  // Map key: `${srcID}->${dstID}`; value: {ok, summary, missing}
  const bridgeState = new Map();

  function bridgeKey(srcID, dstID) { return `${srcID}->${dstID}`; }

  // Recompute one edge's bridge state, update bridgeState, return result.
  async function bridgeRecompute(srcID, dstID) {
    const srcMeta = nodeCEMeta.get(srcID);
    const dstMeta = nodeCEMeta.get(dstID);
    if (!srcMeta || !dstMeta) {
      bridgeState.delete(bridgeKey(srcID, dstID));
      return null;
    }
    const srcSlug = `${srcMeta.workflow_slug}/${srcMeta.stage_slug}`;
    const dstSlug = `${dstMeta.workflow_slug}/${dstMeta.stage_slug}`;
    const [srcSpec, dstSpec] = await Promise.all([
      fetchContract(srcSlug),
      fetchContract(dstSlug),
    ]);
    const result = bridgeCheckSpecs(srcSpec, dstSpec);
    bridgeState.set(bridgeKey(srcID, dstID), result);
    return result;
  }

  // Recompute ALL edges (called after major graph changes).
  async function bridgeRecomputeAll() {
    if (!editor) return;
    const data = editor.export().drawflow.Home.data;
    const pending = [];
    for (const nodeID in data) {
      const node = data[nodeID];
      if (!node.outputs) continue;
      for (const outKey in node.outputs) {
        const conns = node.outputs[outKey].connections || [];
        for (const c of conns) {
          const srcID = Number(nodeID);
          const dstID = Number(c.node);
          pending.push(bridgeRecompute(srcID, dstID));
        }
      }
    }
    await Promise.all(pending);
    renderAllBridgeBadges();
  }

  // Walk DOM, find each Drawflow connection path, place/update the badge.
  function renderAllBridgeBadges() {
    const container = document.getElementById('drawflow');
    if (!container) return;
    const paths = container.querySelectorAll('.connection .main-path');
    paths.forEach((path) => {
      const conn = path.closest('.connection');
      if (!conn) return;
      // Drawflow tags connections with classes like
      //   "connection node_in_node-3 node_out_node-1 ..."
      // We parse to extract src + dst node IDs.
      const ids = parseConnectionEndpoints(conn);
      if (!ids) return;
      const state = bridgeState.get(bridgeKey(ids.src, ids.dst));
      placeBadge(conn, path, ids, state);
    });
  }

  function parseConnectionEndpoints(connEl) {
    const classes = (connEl.getAttribute('class') || '').split(/\s+/);
    let src = null;
    let dst = null;
    for (const c of classes) {
      // formats: node_out_node-1, node_in_node-3
      const mOut = /^node_out_node-(\d+)$/.exec(c);
      if (mOut) src = Number(mOut[1]);
      const mIn = /^node_in_node-(\d+)$/.exec(c);
      if (mIn) dst = Number(mIn[1]);
    }
    if (src == null || dst == null) return null;
    return { src, dst };
  }

  // Compute actual midpoint of an SVG path. Uses getPointAtLength which
  // returns the visual midpoint of the bezier curve — not the average of
  // start/end (which only works for straight lines and floats the badge
  // off-line on curved Drawflow connections).
  function pathMidpoint(path) {
    try {
      const total = path.getTotalLength();
      if (total > 0) {
        const p = path.getPointAtLength(total / 2);
        return { x: p.x, y: p.y };
      }
    } catch (e) { /* fall through to endpoint average */ }
    // Fallback: average of first + last endpoints from the `d` attribute
    const d = path.getAttribute('d') || '';
    const numbers = d.match(/-?\d+(\.\d+)?/g);
    if (!numbers || numbers.length < 4) return { x: 0, y: 0 };
    const x0 = parseFloat(numbers[0]);
    const y0 = parseFloat(numbers[1]);
    const x1 = parseFloat(numbers[numbers.length - 2]);
    const y1 = parseFloat(numbers[numbers.length - 1]);
    return { x: (x0 + x1) / 2, y: (y0 + y1) / 2 };
  }

  function placeBadge(connEl, path, ids, state) {
    // Find or create the badge group inside this connection's SVG
    let badge = connEl.querySelector('.wf-bridge-badge');
    if (!badge) {
      const ns = 'http://www.w3.org/2000/svg';
      badge = document.createElementNS(ns, 'g');
      badge.setAttribute('class', 'wf-bridge-badge');
      const circle = document.createElementNS(ns, 'circle');
      circle.setAttribute('r', '11');
      circle.setAttribute('class', 'wf-bridge-circle');
      const text = document.createElementNS(ns, 'text');
      text.setAttribute('text-anchor', 'middle');
      text.setAttribute('dominant-baseline', 'central');
      text.setAttribute('class', 'wf-bridge-glyph');
      badge.appendChild(circle);
      badge.appendChild(text);
      connEl.appendChild(badge);

      // WP-AO-56 hotfix — click toggles delete-mode on the parent connection.
      // NB: no stopPropagation — Drawflow has its own click handlers we must
      // not block (else the line render state gets desynced on load).
      badge.addEventListener('click', () => {
        const conn = badge.parentElement;
        if (!conn) return;
        if (conn.classList.contains('wf-delete-mode')) {
          // Second click in delete-mode → perform delete
          tryRemoveConnection(ids);
        } else {
          // First click on badge → enter delete-mode (already visible)
          enterDeleteMode(conn, badge);
        }
      });
    }
    // Stash node-id pair on the badge so handlers can pull it without re-parsing
    badge.dataset.srcId = String(ids.src);
    badge.dataset.dstId = String(ids.dst);

    const { x, y } = pathMidpoint(path);
    badge.setAttribute('transform', `translate(${x}, ${y})`);

    const circle = badge.querySelector('.wf-bridge-circle');
    const text = badge.querySelector('.wf-bridge-glyph');
    let glyph = '?';
    let cls = 'wf-bridge-unknown';
    let tooltip = 'bridge: ' + (state ? state.summary : 'unknown');
    if (state) {
      if (state.ok === true) {
        glyph = '○';
        cls = 'wf-bridge-ok';
        tooltip = `bridge ok — ${state.summary}`;
      } else if (state.ok === false) {
        glyph = '✕';
        cls = 'wf-bridge-bad';
        tooltip = `bridge mismatch — ${state.summary}` +
          (state.missing && state.missing.length
            ? `\nmissing: ${state.missing.join(', ')}`
            : '');
      }
    }
    badge.setAttribute('class', 'wf-bridge-badge ' + cls);
    text.textContent = glyph;
    // <title> child for native hover tooltip
    let title = badge.querySelector('title');
    if (!title) {
      title = document.createElementNS('http://www.w3.org/2000/svg', 'title');
      badge.appendChild(title);
    }
    title.textContent = tooltip;
  }

  // Remove .wf-delete-mode from every connection (single-line delete-mode
  // semantics — clicking another line or canvas clears the previous one).
  function clearAllDeleteModes() {
    document.querySelectorAll('#drawflow .connection.wf-delete-mode').forEach((el) => {
      el.classList.remove('wf-delete-mode');
    });
  }

  // Activate delete-mode on one connection: glyph forced to ✕ since the
  // base badge text may have been set to ○/?/✕ by bridgeCheck render.
  function enterDeleteMode(conn, badge) {
    clearAllDeleteModes();
    conn.classList.add('wf-delete-mode');
    const glyph = badge.querySelector('.wf-bridge-glyph');
    if (glyph) glyph.textContent = '✕';
  }

  // Perform the actual Drawflow connection-removal call. Confirm dialog
  // intentionally omitted — second click on the red X is itself the confirm.
  function tryRemoveConnection(ids) {
    try {
      editor.removeSingleConnection(ids.src, ids.dst, 'output_1', 'input_1');
    } catch (err) {
      console.warn('[wf-canvas] removeSingleConnection failed:', err);
    }
    clearAllDeleteModes();
  }

  // WP-AO-56 U1 — richer bridge check returning structured result for badge UI.
  // {ok, missing: [...], extra: [...], summary: "N/M fields"}
  function bridgeCheckSpecs(srcSpec, dstSpec) {
    if (!srcSpec || !dstSpec) return { ok: null, summary: 'spec unavailable' };
    const srcB = schemaKeys(srcSpec.b);
    const dstA = schemaKeys(dstSpec.a);
    if (srcB === null || dstA === null) {
      return { ok: null, summary: 'schemas not field-shaped' };
    }
    const srcSet = new Set(srcB);
    const missing = dstA.filter((k) => !srcSet.has(k));
    if (missing.length === 0) {
      return { ok: true, summary: `${dstA.length} field(s) satisfied`, missing: [], extra: [] };
    }
    return {
      ok: false,
      summary: `missing ${missing.length}/${dstA.length}`,
      missing,
      extra: [],
    };
  }

  async function onConnectionCreated(info) {
    // Drawflow's connectionCreated event: { output_id, input_id, output_class, input_class }
    const srcID = Number(info.output_id);
    const dstID = Number(info.input_id);
    const srcMeta = nodeCEMeta.get(srcID);
    const dstMeta = nodeCEMeta.get(dstID);
    if (!srcMeta || !dstMeta) return;

    const srcSlug = `${srcMeta.workflow_slug}/${srcMeta.stage_slug}`;
    const dstSlug = `${dstMeta.workflow_slug}/${dstMeta.stage_slug}`;

    const [srcSpec, dstSpec] = await Promise.all([
      fetchContract(srcSlug),
      fetchContract(dstSlug),
    ]);
    if (!srcSpec || !dstSpec) {
      toast('Could not fetch CE contracts — connection allowed loosely', 'warn');
      return;
    }

    const cmp = compareSchemas(srcSpec.b, dstSpec.a);
    if (!cmp.ok) {
      // Reject the connection
      try {
        editor.removeSingleConnection(
          info.output_id, info.input_id,
          info.output_class, info.input_class
        );
      } catch (e) {
        // Drawflow returned non-null but the removal might fail silently;
        // either way the toast carries the user message.
      }
      toast(`Schema mismatch: ${cmp.reason}`, 'danger', 4000);
    } else if (cmp.reason !== '') {
      toast(`Edge OK — ${cmp.reason}`, 'ok', 1800);
    }
  }

  // ── CE registry fetch ────────────────────────────────────────────
  async function loadRegistry() {
    const list = $('#wf-palette-list');
    try {
      const res = await fetch('/api/crafter/ce-registry', {
        headers: { 'X-Access-Key': authKey },
      });
      if (!res.ok) {
        list.innerHTML = `<div class="wf-palette-empty">Failed (${res.status})</div>`;
        return;
      }
      const body = await res.json();
      ceRegistry = body.ces || [];
      $('#wf-palette-count').textContent = ceRegistry.length;
      renderPalette('');
    } catch (e) {
      console.error('[wf-canvas] registry fetch:', e);
      list.innerHTML = `<div class="wf-palette-empty">Network error</div>`;
    }
  }

  // ── Palette rendering + type-ahead search ────────────────────────
  function renderPalette(filterText) {
    const list = $('#wf-palette-list');
    const q = (filterText || '').trim().toLowerCase();
    const matches = ceRegistry.filter((ce) => {
      if (!q) return true;
      return (
        ce.workflow_slug.includes(q) ||
        ce.stage_slug.includes(q) ||
        (ce.contract_title || '').toLowerCase().includes(q)
      );
    });

    if (matches.length === 0) {
      list.innerHTML = `<div class="wf-palette-empty">No CEs match "${q}"</div>`;
      return;
    }

    list.innerHTML = matches
      .map((ce) => {
        const slug = `${ce.workflow_slug}/${ce.stage_slug}`;
        const model = ce.vultr_model || 'default';
        return `
          <div class="wf-palette-card" draggable="true" data-ce-slug="${slug}">
            <div class="wf-palette-slug">${escapeHTML(slug)}</div>
            <div class="wf-palette-model">${escapeHTML(model)}</div>
          </div>
        `;
      })
      .join('');

    // Wire dragstart for each card
    list.querySelectorAll('.wf-palette-card').forEach((el) => {
      el.addEventListener('dragstart', onPaletteDragStart);
    });
  }

  // ── Drag-drop wiring ─────────────────────────────────────────────
  let dragCESlug = null;

  function onPaletteDragStart(e) {
    dragCESlug = e.currentTarget.getAttribute('data-ce-slug');
    e.dataTransfer.effectAllowed = 'copy';
    e.dataTransfer.setData('text/plain', dragCESlug); // for cross-browser compat
  }

  function wireCanvasDrop() {
    const drawflowEl = document.getElementById('drawflow');

    drawflowEl.addEventListener('dragover', (e) => {
      e.preventDefault();
      e.dataTransfer.dropEffect = 'copy';
    });

    drawflowEl.addEventListener('drop', (e) => {
      e.preventDefault();
      const slug =
        dragCESlug || e.dataTransfer.getData('text/plain') || '';
      if (!slug) return;

      const ce = ceRegistry.find(
        (c) => `${c.workflow_slug}/${c.stage_slug}` === slug
      );
      if (!ce) {
        toast(`CE "${slug}" not found in registry`, 'warn');
        return;
      }

      // Drop position relative to canvas, accounting for Drawflow's
      // editor.zoom + editor.canvas_x/y translation.
      const rect = drawflowEl.getBoundingClientRect();
      const zoom = editor.zoom || 1;
      const x = (e.clientX - rect.left - editor.canvas_x) / zoom - 130;
      const y = (e.clientY - rect.top  - editor.canvas_y) / zoom - 40;

      const html = ceCardHTML(ce);
      const drawflowID = editor.addNode(
        'ce-card',       // name
        1, 1,            // inputs, outputs
        x, y,
        'ce-card',
        { ce_slug: slug, ce_meta: ce },
        html
      );
      nodeCEMeta.set(drawflowID, ce);

      dragCESlug = null;
    });
  }

  // ── CE-card HTML template (mirrors WP-AO-37 spike pattern) ───────
  function ceCardHTML(ce) {
    const slug = `${ce.workflow_slug}/${ce.stage_slug}`;
    const model = ce.vultr_model || 'default';
    const l0 = Number.isFinite(ce.trust_gate_l0) ? ce.trust_gate_l0 : 0;
    const l1 = Number.isFinite(ce.trust_gate_l1) ? ce.trust_gate_l1 : 0;
    const l2 = Number.isFinite(ce.trust_gate_l2) ? ce.trust_gate_l2 : 0;
    const l3 = Number.isFinite(ce.trust_gate_l3) ? ce.trust_gate_l3 : 0;
    const pre = ce.p_pre_count || 0;
    const post = ce.p_post_count || 0;
    // WP-01 U5: trust-gate chips are clickable; click opens the layer popup
    // modal IFF the chip has a stored verdict that's DENY/LOG (data-clickable).
    // ALLOW verdicts produce a tiny toast on click ("no issues"). The data-*
    // attrs let the global click handler dispatch to openLayerModal without
    // per-chip bindings.
    return `
      <div class="ce-card">
        <h4 class="ce-slug">${escapeHTML(slug)}</h4>
        <div class="ce-model">${escapeHTML(model)}</div>
        <div class="chips">
          <span class="chip l0 wf-trust-chip" data-layer="L0" data-ce-slug="${escapeHTML(slug)}">L0 ${l0}</span>
          <span class="chip l1 wf-trust-chip" data-layer="L1" data-ce-slug="${escapeHTML(slug)}">L1 ${l1}</span>
          <span class="chip l2 wf-trust-chip" data-layer="L2" data-ce-slug="${escapeHTML(slug)}">L2 ${l2}</span>
          <span class="chip l3 wf-trust-chip" data-layer="L3" data-ce-slug="${escapeHTML(slug)}">L3 ${l3}</span>
          <span class="chip pre" onclick="event.stopPropagation();">P_pre · ${pre}</span>
          <span class="chip post" onclick="event.stopPropagation();">P_post · ${post}</span>
        </div>
        <div class="ce-card-viewer-strip" data-ce-slug="${escapeHTML(slug)}" title="Open CE viewer (test this CE in isolation)">
          ▶ CE viewer
        </div>
      </div>
    `;
  }

  // ── WP-01 U5: per-node latest-layer-results store ────────────────
  // Map<drawflowNodeID:number, { L0: evt, L1: evt, L2: evt, L3: evt }>
  // Updated on every l*_completed RunEvent so chip clicks have data to show.
  const latestLayerResults = new Map();
  function storeLayerResult(nodeID, layer, evt) {
    const dfID = parseInt(String(nodeID || '').replace(/^n/, ''), 10);
    if (!dfID && dfID !== 0) return;
    let rec = latestLayerResults.get(dfID);
    if (!rec) { rec = {}; latestLayerResults.set(dfID, rec); }
    rec[layer] = evt;
  }

  // ── Toast helper ─────────────────────────────────────────────────
  function toast(msg, kind = 'ok', durationMs = 2500) {
    const container = document.getElementById('wf-toasts');
    if (!container) return;
    const el = document.createElement('div');
    el.className = `wf-toast ${kind}`;
    el.textContent = msg;
    container.appendChild(el);
    setTimeout(() => {
      el.style.opacity = '0';
      el.style.transition = 'opacity 0.3s';
      setTimeout(() => el.remove(), 300);
    }, durationMs);
  }

  // ── HTML escape helper ───────────────────────────────────────────
  function escapeHTML(s) {
    return String(s || '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  // ── Trace panel collapse toggle (chrome only — Unit 6 fills body) ─
  function wireTraceToggle() {
    const tray = document.getElementById('wf-trace');
    const toggle = document.getElementById('wf-trace-toggle');
    if (!tray || !toggle) return;
    toggle.addEventListener('click', () => {
      tray.classList.toggle('collapsed');
    });
  }

  // ── Save / Load wiring (Unit 5) ──────────────────────────────────
  function wireSaveLoadButtons() {
    $('#wf-save-btn')?.addEventListener('click', doSave);
    $('#wf-load-btn')?.addEventListener('click', showLoadDialog);
    $('#wf-run-btn') ?.addEventListener('click', doRun);
  }

  // ── Run + SSE trace (Unit 6) ─────────────────────────────────────
  /** Active EventSource (only one run at a time in v1). */
  let activeEventSource = null;
  /** Current run_id for the in-flight run. */
  let currentRunID = null;
  /** Trace event counter for the current run. */
  let traceEventCount = 0;

  async function doRun() {
    if (activeEventSource) {
      toast('A run is already in flight', 'warn');
      return;
    }
    const slug = ($('#wf-slug-input')?.value || '').trim() || 'untitled';
    const title = ($('#wf-title-input')?.value || '').trim() || slug;
    const drawflowExport = editor.export();
    const wf = drawflowToCanonical(drawflowExport, slug, title);

    if (wf.nodes.length === 0) {
      toast('Empty canvas — nothing to run', 'warn');
      return;
    }
    if (!wf.entry_node || !wf.exit_node) {
      toast('Could not derive entry/exit nodes — check edges', 'warn');
      return;
    }

    // Clear previous run state
    clearRunState();
    setRunButtonLabel('⏵ Running…', true);
    progressReset((wf.nodes || []).length);

    let runID;
    try {
      const res = await fetch('/api/workflow/run', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Access-Key': authKey,
        },
        body: JSON.stringify({ workflow: wf, input: getSelectedScenarioInput() || {} }),
      });
      if (!res.ok) {
        const body = await res.text();
        toast(`Run start failed (${res.status}): ${body}`, 'danger', 4500);
        setRunButtonLabel('▶ Run', false);
        return;
      }
      const data = await res.json();
      runID = data.run_id;
    } catch (e) {
      toast(`Run network error: ${e.message}`, 'danger');
      setRunButtonLabel('▶ Run', false);
      return;
    }

    currentRunID = runID;
    const traceMeta = $('#wf-trace-meta');
    if (traceMeta) traceMeta.textContent = `run ${runID} · live`;
    // Open the trace panel if collapsed
    $('#wf-trace')?.classList.remove('collapsed');

    const url = `/api/workflow/run/stream?run_id=${encodeURIComponent(runID)}&key=${encodeURIComponent(authKey)}`;
    const es = new EventSource(url);
    activeEventSource = es;

    es.addEventListener('progress', (e) => {
      try {
        const evt = JSON.parse(e.data);
        handleRunEvent(evt);
      } catch (err) {
        console.error('[wf-canvas] progress parse:', err, e.data);
      }
    });

    es.addEventListener('complete', (e) => {
      let finalStatus = 'UNKNOWN';
      try {
        const payload = JSON.parse(e.data);
        finalStatus = payload.final_status || 'UNKNOWN';
        showFinalBanner(finalStatus);
      } catch (err) {
        showFinalBanner('UNKNOWN');
      }
      progressFinal(finalStatus);
      es.close();
      activeEventSource = null;
      setRunButtonLabel('▶ Run', false);
      const traceMeta = $('#wf-trace-meta');
      if (traceMeta) traceMeta.textContent = `run ${runID} · complete`;
    });

    es.addEventListener('error', (e) => {
      // EventSource keeps retrying on transient errors; only show the toast
      // if we lose the connection persistently.
      console.warn('[wf-canvas] SSE error event', e);
    });
  }

  function clearRunState() {
    traceEventCount = 0;
    document.querySelectorAll('.drawflow .drawflow-node').forEach((el) => {
      el.classList.remove('wf-running',
        'wf-l0-pass', 'wf-l0-deny',
        'wf-l1-pass', 'wf-l1-deny',
        'wf-l2-success', 'wf-l2-failure',
        'wf-l3-pass', 'wf-l3-deny');
    });
    latestLayerResults.clear();
    const body = $('#wf-trace-body');
    if (body) body.innerHTML = '';
    const banner = $('#wf-trace-banner');
    if (banner) {
      banner.className = 'wf-trace-banner hidden';
      banner.textContent = '';
    }
    // WP-AO-59 — drop stale active-row references from the previous run
    activeRows.clear();
    // WP-AO-60 — drop node-group references too
    nodeGroups.clear();
  }

  // ── WP-AO-60: per-node trace grouping ──────────────────────────────
  // Map<nodeID, { section, header, bodyEl, statusPill }>
  const nodeGroups = new Map();

  function getOrCreateNodeGroup(nodeID, ceSlug) {
    if (!nodeID) return null;
    if (nodeGroups.has(nodeID)) return nodeGroups.get(nodeID);
    const traceBody = document.getElementById('wf-trace-body');
    if (!traceBody) return null;
    // Drop empty-state placeholder on first group creation
    const empty = traceBody.querySelector('.wf-trace-empty');
    if (empty) empty.remove();

    const section = document.createElement('section');
    section.className = 'wf-trace-node-group running';
    section.dataset.nodeId = nodeID;

    const header = document.createElement('div');
    header.className = 'wf-trace-node-header';
    header.innerHTML = `
      <button class="wf-trace-chevron-btn" title="Collapse/expand">▼</button>
      <span class="wf-trace-node-id">${escapeHTML(nodeID)}</span>
      <span class="wf-trace-node-slug">${escapeHTML(ceSlug || '')}</span>
      <span class="wf-trace-node-status">running…</span>
    `;
    header.addEventListener('click', () => {
      section.classList.toggle('wf-trace-collapsed');
    });

    const bodyEl = document.createElement('div');
    bodyEl.className = 'wf-trace-node-body';

    section.appendChild(header);
    section.appendChild(bodyEl);
    traceBody.appendChild(section);

    const entry = {
      section,
      header,
      bodyEl,
      statusPill: header.querySelector('.wf-trace-node-status'),
    };
    nodeGroups.set(nodeID, entry);
    return entry;
  }

  // Update the header status pill + state class based on a phase event.
  function updateNodeGroupStatus(nodeID, evt) {
    const grp = nodeGroups.get(nodeID);
    if (!grp) return;
    const section = grp.section;
    const pill = grp.statusPill;
    const verdict = evt.verdict || '';
    if (evt.error) {
      pill.textContent = `${(evt.phase || '').toUpperCase()} ERROR`;
      section.classList.remove('running', 'l1-pass', 'l2-success');
      section.classList.add('error');
      return;
    }
    if (evt.phase === 'l0') {
      if (verdict === 'ALLOW' || verdict === 'SKIPPED') {
        pill.textContent = `L0 ${verdict}`;
        section.classList.remove('error');
      } else if (verdict === 'DENY') {
        pill.textContent = `L0 DENY`;
        section.classList.add('error');
      }
    } else if (evt.phase === 'l3') {
      if (verdict === 'ALLOW' || verdict === 'SKIPPED') {
        pill.textContent = `L3 ${verdict}`;
        section.classList.add('wf-trace-collapsed');
      } else if (verdict === 'DENY') {
        pill.textContent = `L3 DENY`;
        section.classList.remove('wf-trace-collapsed');
        section.classList.add('error');
      }
    } else if (evt.phase === 'l1') {
      if (verdict === 'ALLOW' || verdict === 'SKIPPED') {
        pill.textContent = `L1 ${verdict}${evt.score ? ' · ' + evt.score : ''}`;
        section.classList.remove('error', 'l2-failure');
        section.classList.add('l1-pass');
      } else if (verdict === 'DENY') {
        pill.textContent = `L1 DENY${evt.score ? ' · ' + evt.score : ''}`;
        section.classList.remove('l1-pass', 'l2-success');
        section.classList.add('error');
      }
    } else if (evt.phase === 'exec') {
      // only acknowledge completion (state==completed), not running
      if (evt.state !== 'running') {
        pill.textContent = 'EXEC done';
      }
    } else if (evt.phase === 'l2') {
      if (verdict === 'SUCCESS' || verdict === 'SKIPPED') {
        pill.textContent = `L2 ${verdict}${evt.score ? ' · ' + evt.score : ''}`;
        section.classList.remove('running', 'error', 'l2-failure');
        section.classList.add('l2-success');
        // WP-01 fix 2026-05-19: do NOT auto-collapse on L2 SUCCESS — L3 still
        // has to run + emit. Collapsing here hides the L3 row inside the
        // collapsed group. Move the auto-collapse to L3 completion.
      } else if (verdict === 'FAILURE') {
        pill.textContent = `L2 FAILURE${evt.score ? ' · ' + evt.score : ''}`;
        section.classList.remove('running', 'l1-pass', 'l2-success');
        section.classList.add('l2-failure');
        section.classList.remove('wf-trace-collapsed');
      }
    }
  }

  function setRunButtonLabel(text, disabled) {
    const btn = $('#wf-run-btn');
    if (!btn) return;
    btn.textContent = text;
    btn.disabled = !!disabled;
  }

  function handleRunEvent(evt) {
    // evt fields: phase, node_id, ce_slug, verdict, score, reasons, latency_ms, error, state, meta
    traceEventCount++;
    if (evt.node_id) {
      const drawflowID = parseInt(String(evt.node_id).replace(/^n/, ''), 10);
      applyNodeClass(drawflowID, evt);
      // WP-01 U5 — record latest L0/L1/L2/L3 result for chip-click popup.
      if (evt.state === 'completed' || (evt.verdict && !evt.state)) {
        let layerKey = '';
        if (evt.phase === 'l0') layerKey = 'L0';
        else if (evt.phase === 'l1') layerKey = 'L1';
        else if (evt.phase === 'l2') layerKey = 'L2';
        else if (evt.phase === 'l3') layerKey = 'L3';
        if (layerKey) {
          storeLayerResult(evt.node_id, layerKey, evt);
          // Auto-popup on negative verdict (David spec 2026-05-19:
          // "Popup for negative or issue"). DENY surfaces the modal
          // immediately; FAILURE on L2 also pops; ALLOW/SUCCESS/SKIPPED stay
          // silent (they're already visible in the run trace).
          const v = (evt.verdict || '').toUpperCase();
          if (v === 'DENY' || v === 'FAILURE') {
            openLayerModal(layerKey, evt.ce_slug || '', evt);
          }
        }
      }
    }

    // WP-AO-59 — phase-start events render an animated active row keyed by
    // (node_id, phase); the completion event updates the row in place.
    if (evt.state === 'running' && (evt.phase === 'l0' || evt.phase === 'l1' || evt.phase === 'exec' || evt.phase === 'l2' || evt.phase === 'l3')) {
      appendActiveTraceRow(evt);
      return;
    }

    appendTraceRow(evt);
    // WP-AO-58 — advance the top progress bar on each layer completion.
    if ((evt.phase === 'l0' || evt.phase === 'l1' || evt.phase === 'exec' || evt.phase === 'l2' || evt.phase === 'l3') &&
        evt.state !== 'running') {
      progressIncrement();
    }
  }

  // WP-AO-59 — active-row registry: a row created on state="running" stays
  // animated until the matching completion event arrives.
  const activeRows = new Map(); // key: `${node_id}|${phase}` → HTMLElement

  function activeRowKey(evt) { return `${evt.node_id || '_'}|${evt.phase}`; }

  function appendActiveTraceRow(evt) {
    const traceBody = document.getElementById('wf-trace-body');
    if (!traceBody) return;
    const empty = traceBody.querySelector('.wf-trace-empty');
    if (empty) empty.remove();

    // WP-AO-60 — append into the node's group body (creates group if missing)
    const grp = getOrCreateNodeGroup(evt.node_id, evt.ce_slug);
    const target = grp ? grp.bodyEl : traceBody;

    const row = document.createElement('div');
    row.className = 'wf-trace-row wf-active';
    const time = (evt.timestamp || '').slice(11, 23);
    const phase = (evt.phase || '').toUpperCase();
    const node = evt.node_id || '—';
    row.innerHTML = `
      <span class="tr-time">${escapeHTML(time)}</span>
      <span class="tr-phase">${escapeHTML(phase)}</span>
      <span class="tr-node">${escapeHTML(node)}</span>
      <span class="tr-verdict">running…</span>
      <span></span>
    `;
    target.appendChild(row);
    traceBody.scrollTop = traceBody.scrollHeight;
    activeRows.set(activeRowKey(evt), row);
  }

  // ── WP-AO-58: Run progress bar state machine ─────────────────────
  let progressExpected = 0;
  let progressCompleted = 0;

  function progressReset(totalNodes) {
    const el = document.getElementById('wf-progress');
    const fill = document.getElementById('wf-progress-fill');
    if (!el || !fill) return;
    progressExpected = Math.max(1, (totalNodes || 0) * 5); // L0 + L1 + exec + L2 + L3 per node
    progressCompleted = 0;
    el.classList.remove('hidden', 'success', 'failure');
    el.classList.add('running');
    el.setAttribute('aria-valuenow', '0');
    fill.style.width = '0%';
  }

  function progressIncrement() {
    const el = document.getElementById('wf-progress');
    const fill = document.getElementById('wf-progress-fill');
    if (!el || !fill || progressExpected === 0) return;
    progressCompleted = Math.min(progressExpected, progressCompleted + 1);
    const pct = Math.round((progressCompleted / progressExpected) * 100);
    el.setAttribute('aria-valuenow', String(pct));
    fill.style.width = pct + '%';
  }

  function progressFinal(finalStatus) {
    const el = document.getElementById('wf-progress');
    const fill = document.getElementById('wf-progress-fill');
    if (!el || !fill) return;
    el.classList.remove('running');
    fill.style.width = '100%';
    el.setAttribute('aria-valuenow', '100');
    const ok = finalStatus === 'SUCCESS';
    el.classList.add(ok ? 'success' : 'failure');
    if (ok) {
      // Auto-fade after a moment so the bar doesn't linger
      setTimeout(() => {
        const cur = document.getElementById('wf-progress');
        if (cur) cur.classList.add('hidden');
      }, 1500);
    }
    // Failure path: bar stays visible (red) until next Run resets it
  }

  function applyNodeClass(drawflowID, evt) {
    const el = document.querySelector(`#drawflow .drawflow-node[id="node-${drawflowID}"]`);
    if (!el) return;
    // Reset just the dynamic classes; preserve user state classes
    el.classList.remove('wf-running');
    if (evt.phase === 'l0') {
      el.classList.remove('wf-l0-pass', 'wf-l0-deny');
      if (evt.verdict === 'ALLOW' || evt.verdict === 'SKIPPED') {
        el.classList.add('wf-l0-pass');
      } else if (evt.verdict) {
        el.classList.add('wf-l0-deny');
      }
    } else if (evt.phase === 'l1') {
      el.classList.remove('wf-l1-pass', 'wf-l1-deny');
      if (evt.verdict === 'ALLOW') {
        el.classList.add('wf-l1-pass');
      } else if (evt.verdict) {
        el.classList.add('wf-l1-deny');
      }
    } else if (evt.phase === 'exec') {
      el.classList.add('wf-running');
    } else if (evt.phase === 'l2') {
      el.classList.remove('wf-l2-success', 'wf-l2-failure');
      if (evt.verdict === 'SUCCESS') {
        el.classList.add('wf-l2-success');
      } else if (evt.verdict) {
        el.classList.add('wf-l2-failure');
      }
    } else if (evt.phase === 'l3') {
      el.classList.remove('wf-l3-pass', 'wf-l3-deny');
      if (evt.verdict === 'ALLOW' || evt.verdict === 'SKIPPED') {
        el.classList.add('wf-l3-pass');
      } else if (evt.verdict) {
        el.classList.add('wf-l3-deny');
      }
    }
  }

  function appendTraceRow(evt) {
    const traceBody = $('#wf-trace-body');
    if (!traceBody) return;
    // Drop the empty-state placeholder on first event
    const empty = traceBody.querySelector('.wf-trace-empty');
    if (empty) empty.remove();

    // WP-AO-60 — node-scoped events route into the node's group body;
    // global events (start/end/halt with no node_id) stay flat at the root.
    const grp = evt.node_id ? getOrCreateNodeGroup(evt.node_id, evt.ce_slug) : null;
    const target = grp ? grp.bodyEl : traceBody;

    // WP-AO-59 — if a phase-start ("running") row exists for this
    // (node_id, phase), replace its content in place rather than appending.
    // WP-01 U5 fix (2026-05-19): include l0/l3 so their "running…" rows get
    // swapped in place on completion instead of getting a duplicate ALLOW row.
    if (evt.phase === 'l0' || evt.phase === 'l1' || evt.phase === 'exec' || evt.phase === 'l2' || evt.phase === 'l3') {
      const key = activeRowKey(evt);
      const activeRow = activeRows.get(key);
      if (activeRow) {
        activeRows.delete(key);
        const time = (evt.timestamp || '').slice(11, 23);
        const phase = (evt.phase || '').toUpperCase();
        const node = evt.node_id || '—';
        const verdict = evt.verdict || (evt.error ? 'ERROR' : '—');
        const verdictClass = evt.verdict === 'ALLOW' || evt.verdict === 'SUCCESS'
          ? 'ok'
          : (evt.verdict ? 'deny' : '');
        let reasonsHTML = '';
        if (Array.isArray(evt.reasons) && evt.reasons.length > 0) {
          const items = evt.reasons.map((r) => `<li>${escapeHTML(r)}</li>`).join('');
          reasonsHTML = `<details class="tr-reasons"><summary>${evt.reasons.length} reason(s)</summary><ul>${items}</ul></details>`;
        } else if (evt.error) {
          reasonsHTML = `<span class="tr-reasons" style="color:var(--red);">${escapeHTML(evt.error)}</span>`;
        }
        activeRow.classList.remove('wf-active');
        activeRow.innerHTML = `
          <span class="tr-time">${escapeHTML(time)}</span>
          <span class="tr-phase">${escapeHTML(phase)}</span>
          <span class="tr-node">${escapeHTML(node)}</span>
          <span class="tr-verdict ${verdictClass}">${escapeHTML(verdict)}${evt.score ? ' · ' + evt.score : ''}</span>
          <span>${reasonsHTML}</span>
        `;
        updateNodeGroupStatus(evt.node_id, evt);
        traceBody.scrollTop = traceBody.scrollHeight;
        return;
      }
    }

    // WP-AO-53 U5 — tool-call event has its own compact row format.
    if (evt.phase === 'tool') {
      const row = document.createElement('div');
      row.className = 'wf-trace-row wf-trace-tool';
      const time = (evt.timestamp || '').slice(11, 23);
      const tool = evt.tool || '?';
      const args = evt.tool_args == null ? '' : compactArgs(evt.tool_args);
      const summary = evt.tool_summary || (evt.error ? 'error' : 'ok');
      const latency = evt.latency_ms != null ? ` · ${evt.latency_ms}ms` : '';
      const errSpan = evt.error ? ` <span class="tr-err">⚠ ${escapeHTML(evt.error)}</span>` : '';
      row.innerHTML = `
        <span class="tr-time">${escapeHTML(time)}</span>
        <span class="tr-phase tr-phase-tool">🔧</span>
        <span class="tr-node">${escapeHTML(evt.node_id || '—')}</span>
        <span class="tr-tool"><code>${escapeHTML(tool)}(${escapeHTML(args)})</code> → ${escapeHTML(summary)}${latency}${errSpan}</span>
      `;
      target.appendChild(row);
      traceBody.scrollTop = traceBody.scrollHeight;
      return;
    }

    const row = document.createElement('div');
    row.className = 'wf-trace-row';

    const time = (evt.timestamp || '').slice(11, 23);
    const phase = (evt.phase || '').toUpperCase();
    const node = evt.node_id || '—';
    const verdict = evt.verdict || (evt.error ? 'ERROR' : '—');
    const verdictClass = evt.verdict === 'ALLOW' || evt.verdict === 'SUCCESS'
      ? 'ok'
      : (evt.verdict ? 'deny' : '');

    let reasonsHTML = '';
    if (Array.isArray(evt.reasons) && evt.reasons.length > 0) {
      const items = evt.reasons.map((r) => `<li>${escapeHTML(r)}</li>`).join('');
      reasonsHTML = `<details class="tr-reasons"><summary>${evt.reasons.length} reason(s)</summary><ul>${items}</ul></details>`;
    } else if (evt.error) {
      reasonsHTML = `<span class="tr-reasons" style="color:var(--red);">${escapeHTML(evt.error)}</span>`;
    }

    row.innerHTML = `
      <span class="tr-time">${escapeHTML(time)}</span>
      <span class="tr-phase">${escapeHTML(phase)}</span>
      <span class="tr-node">${escapeHTML(node)}</span>
      <span class="tr-verdict ${verdictClass}">${escapeHTML(verdict)}${evt.score ? ' · ' + evt.score : ''}</span>
      <span>${reasonsHTML}</span>
    `;
    target.appendChild(row);
    if (evt.node_id) updateNodeGroupStatus(evt.node_id, evt);
    traceBody.scrollTop = traceBody.scrollHeight;
  }

  // compactArgs renders a tool args object as a short key=value list, truncated
  // to keep the trace row narrow. e.g. {policy_no:"HIC-...",limit:5} → policy_no=HIC-..., limit=5
  function compactArgs(args) {
    try {
      if (args == null) return '';
      if (typeof args === 'string') return args.length > 60 ? args.slice(0, 60) + '…' : args;
      if (typeof args !== 'object') return String(args);
      const parts = [];
      for (const [k, v] of Object.entries(args)) {
        if (v == null) continue;
        if (k === 'where' && typeof v === 'object') {
          for (const [wk, wv] of Object.entries(v)) {
            parts.push(`${wk}=${truncStr(String(wv), 24)}`);
          }
        } else {
          parts.push(`${k}=${truncStr(String(typeof v === 'object' ? JSON.stringify(v) : v), 24)}`);
        }
        if (parts.join(', ').length > 80) {
          parts[parts.length - 1] += '…';
          break;
        }
      }
      return parts.join(', ');
    } catch (e) {
      return '';
    }
  }
  function truncStr(s, n) { return s.length > n ? s.slice(0, n) + '…' : s; }

  function showFinalBanner(finalStatus) {
    const banner = $('#wf-trace-banner');
    if (!banner) return;
    let cls = 'failure';
    let txt = `Halted: ${finalStatus}`;
    if (finalStatus === 'SUCCESS') {
      cls = 'success';
      txt = 'Workflow completed — all L1/L2 gates passed';
    } else if (finalStatus === 'HALTED_L1') {
      txt = 'Halted at L1 P-check — input rejected';
    } else if (finalStatus === 'HALTED_L2') {
      txt = 'Halted at L2 O-check — output rejected';
    } else if (finalStatus === 'CYCLE_ERROR') {
      txt = 'Workflow has a cycle — fix the DAG';
    }
    banner.className = `wf-trace-banner ${cls}`;
    banner.textContent = txt;
  }

  async function doSave() {
    const slug = ($('#wf-slug-input')?.value || '').trim();
    if (!/^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$|^[a-z0-9]$/.test(slug)) {
      toast('Slug must be kebab-case (1-64 chars)', 'warn');
      return;
    }
    const title = ($('#wf-title-input')?.value || '').trim() || slug;
    const drawflowExport = editor.export();
    const wf = drawflowToCanonical(drawflowExport, slug, title);

    if (wf.nodes.length === 0) {
      toast('Empty canvas — nothing to save', 'warn');
      return;
    }
    try {
      const res = await fetch('/api/workflow/save', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Access-Key': authKey,
        },
        body: JSON.stringify({ slug, workflow: wf }),
      });
      if (res.ok) {
        toast(`Saved "${slug}"`, 'ok');
      } else {
        const body = await res.text();
        toast(`Save failed (${res.status}): ${body}`, 'danger', 4000);
      }
    } catch (e) {
      toast(`Save network error: ${e.message}`, 'danger');
    }
  }

  async function showLoadDialog() {
    let list;
    try {
      const res = await fetch('/api/workflow/list', {
        headers: { 'X-Access-Key': authKey },
      });
      if (!res.ok) {
        toast('List failed: ' + res.status, 'danger');
        return;
      }
      const body = await res.json();
      list = body.workflows || [];
    } catch (e) {
      toast('List network error', 'danger');
      return;
    }
    if (list.length === 0) {
      toast('No saved workflows yet', 'warn');
      return;
    }
    // Simple prompt-based picker — keeps the surface tiny + dependency-free.
    const options = list
      .map((w, i) => `${i + 1}. ${w.slug}${w.title ? ' — ' + w.title : ''}`)
      .join('\n');
    const choice = window.prompt(
      'Pick a workflow to load:\n\n' + options + '\n\nEnter number:'
    );
    const idx = parseInt(choice, 10) - 1;
    if (isNaN(idx) || idx < 0 || idx >= list.length) return;
    doLoad(list[idx].slug);
  }

  async function doLoad(slug) {
    try {
      const res = await fetch(
        '/api/workflow/load?slug=' + encodeURIComponent(slug),
        { headers: { 'X-Access-Key': authKey } }
      );
      if (!res.ok) {
        toast('Load failed: ' + res.status, 'danger');
        return;
      }
      const wf = await res.json();
      editor.clearModuleSelected();
      // Drawflow doesn't expose "clear all" cleanly; reset by re-init.
      const drawflowJSON = canonicalToDrawflow(wf);
      // WP-AO-60 hotfix — defer the import to the next animation frame so
      // canvas-wrap is fully sized before Drawflow reads container.offsetWidth
      // during node placement. Without this, fast-clicks during layout
      // transitions can trip "Cannot read properties of undefined".
      await new Promise((resolve) => requestAnimationFrame(resolve));
      editor.import(drawflowJSON);
      // Repopulate nodeCEMeta from imported nodes.
      nodeCEMeta.clear();
      for (const node of wf.nodes) {
        const id = parseInt(node.id.replace(/^n/, ''), 10);
        const meta = ceRegistry.find(
          (c) => `${c.workflow_slug}/${c.stage_slug}` === node.ce_slug
        );
        if (meta) nodeCEMeta.set(id, meta);
      }
      $('#wf-title-input').value = wf.title || '';
      $('#wf-slug-input').value  = wf.workflow_slug || slug;
      // WP-AO-50: restore audit-toggle state from the loaded workflow.json
      // WP-AO-52 U3: mirror runner nil→true default; older saves omit
      // audit_l{1,2} so `!!undefined` would uncheck them — that's a regression.
      const l1Toggle = document.getElementById('wf-audit-l1-toggle');
      const l2Toggle = document.getElementById('wf-audit-l2-toggle');
      if (l1Toggle) l1Toggle.checked = wf.audit_l1 === undefined ? true : !!wf.audit_l1;
      if (l2Toggle) l2Toggle.checked = wf.audit_l2 === undefined ? true : !!wf.audit_l2;
      toast(`Loaded "${slug}"`, 'ok', 2200);
    } catch (e) {
      toast('Load network error: ' + e.message, 'danger');
    }
  }

  // ── Drawflow ↔ canonical shim (mirrors Go workflow_yaml.go §7 rules) ─
  function drawflowToCanonical(drawflowExport, workflowSlug, title) {
    // WP-AO-50: read audit-toggle state into the canonical JSON. Default
    // OFF (unchecked) — workflows run end-to-end without depending on the
    // audit-gate SaaS. User opts IN via the toolbar toggles.
    const l1Toggle = document.getElementById('wf-audit-l1-toggle');
    const l2Toggle = document.getElementById('wf-audit-l2-toggle');
    const auditL1 = l1Toggle ? !!l1Toggle.checked : false;
    const auditL2 = l2Toggle ? !!l2Toggle.checked : false;
    const wf = {
      schema_version: '1.0',
      workflow_slug: workflowSlug,
      title: title,
      created_at: new Date().toISOString(),
      entry_node: '',
      exit_node: '',
      audit_l1: auditL1,
      audit_l2: auditL2,
      nodes: [],
      edges: [],
    };
    const mod =
      (drawflowExport && drawflowExport.drawflow && drawflowExport.drawflow.Home) ||
      null;
    if (!mod || !mod.data) return wf;

    const indeg = new Map();
    const outdeg = new Map();

    const ids = Object.keys(mod.data).sort((a, b) => parseInt(a, 10) - parseInt(b, 10));
    for (const k of ids) {
      const n = mod.data[k];
      const canonicalID = 'n' + n.id;
      wf.nodes.push({
        id: canonicalID,
        ce_slug: (n.data && n.data.ce_slug) || '',
        position: { x: n.pos_x | 0, y: n.pos_y | 0 },
      });
      indeg.set(canonicalID, 0);
      outdeg.set(canonicalID, 0);
    }

    for (const k of ids) {
      const n = mod.data[k];
      const srcID = 'n' + n.id;
      const outKeys = Object.keys(n.outputs || {}).sort();
      for (const outPort of outKeys) {
        const conns = (n.outputs[outPort].connections) || [];
        for (const c of conns) {
          const dstID = 'n' + c.node;
          wf.edges.push({
            from: srcID + '.' + portCanonical(outPort, 'output'),
            to:   dstID + '.' + portCanonical(c.output, 'input'),
          });
          outdeg.set(srcID, (outdeg.get(srcID) || 0) + 1);
          indeg.set(dstID,  (indeg.get(dstID)  || 0) + 1);
        }
      }
    }

    wf.edges.sort((a, b) =>
      a.from === b.from ? (a.to < b.to ? -1 : 1) : a.from < b.from ? -1 : 1
    );

    const entries = wf.nodes.filter((n) => indeg.get(n.id) === 0).map((n) => n.id).sort();
    const exits   = wf.nodes.filter((n) => outdeg.get(n.id) === 0).map((n) => n.id).sort();
    wf.entry_node = entries[0] || '';
    wf.exit_node  = exits[0]   || '';
    return wf;
  }

  function canonicalToDrawflow(wf) {
    const mod = { data: {} };
    for (const n of (wf.nodes || [])) {
      const drawflowID = parseInt(n.id.replace(/^n/, ''), 10);
      const meta = ceRegistry.find(
        (c) => `${c.workflow_slug}/${c.stage_slug}` === n.ce_slug
      );
      const html = meta ? ceCardHTML(meta) : `<div class="ce-card"><div class="ce-slug">${escapeHTML(n.ce_slug)}</div></div>`;
      mod.data[String(drawflowID)] = {
        id: drawflowID,
        name: 'ce-card',
        data: { ce_slug: n.ce_slug, ce_meta: meta || {} },
        class: 'ce-card',
        html: html,
        typenode: false,
        // WP-AO-60 hotfix — Drawflow expects at least input_1 / output_1
        // keys present (with connections: []) even when a node has no
        // edges on that side. Empty {} can crash import on node-positioning
        // ("Cannot read properties of undefined reading offsetWidth").
        inputs:  { input_1:  { connections: [] } },
        outputs: { output_1: { connections: [] } },
        pos_x: (n.position && n.position.x) | 0,
        pos_y: (n.position && n.position.y) | 0,
      };
    }
    for (const e of (wf.edges || [])) {
      const [fromNode, fromPort] = splitPortRef(e.from);
      const [toNode,   toPort]   = splitPortRef(e.to);
      const srcID = String(parseInt(fromNode.replace(/^n/, ''), 10));
      const dstID = String(parseInt(toNode.replace(/^n/, ''),   10));
      const outPort = portDrawflow(fromPort, 'output');
      const inPort  = portDrawflow(toPort,   'input');

      const src = mod.data[srcID];
      const dst = mod.data[dstID];
      if (!src || !dst) continue;

      src.outputs[outPort] = src.outputs[outPort] || { connections: [] };
      src.outputs[outPort].connections.push({ node: dstID, output: inPort });
      dst.inputs[inPort] = dst.inputs[inPort] || { connections: [] };
      dst.inputs[inPort].connections.push({ node: srcID, output: outPort });
    }
    return { drawflow: { Home: mod } };
  }

  function portCanonical(drawflowPortName, side) {
    return drawflowPortName === side + '_1' ? side : drawflowPortName;
  }
  function portDrawflow(canonicalPortName, side) {
    return canonicalPortName === side ? side + '_1' : canonicalPortName;
  }
  function splitPortRef(ref) {
    const idx = ref.indexOf('.');
    return idx < 0 ? [ref, ''] : [ref.slice(0, idx), ref.slice(idx + 1)];
  }

  // ── Palette search ───────────────────────────────────────────────
  function wirePaletteSearch() {
    $('#wf-palette-search')?.addEventListener('input', (e) => {
      renderPalette(e.target.value);
    });
  }

  // ── Title/slug — persisted to localStorage so reloads keep state ──
  function wireTitleSlug() {
    const t = $('#wf-title-input');
    const s = $('#wf-slug-input');
    if (!t || !s) return;
    t.value = localStorage.getItem('wf-title') || '';
    s.value = localStorage.getItem('wf-slug')  || '';
    t.addEventListener('input', () => localStorage.setItem('wf-title', t.value));
    s.addEventListener('input', () => localStorage.setItem('wf-slug',  s.value));
  }

  // ── Boot ─────────────────────────────────────────────────────────
  document.addEventListener('DOMContentLoaded', () => {
    initEditor();
    wireCanvasDrop();
    wirePaletteSearch();
    wireTraceToggle();
    wireSaveLoadButtons();
    wireTitleSlug();
    populateScenarioPicker();           // WP-AO-41: scenario dropdown
    wirePaletteReload();                // WP-AO-49 Unit 3: manual + postMessage reload
    wireLoadDemoBtn();                  // U9: ⚡ Load demo project header button
    wireLayerChipClicks();              // WP-01 U5: trust-gate chip → popup modal
    wireAuditLogModal();                // WP-01: 📋 Audit log button + modal
    wireHamburgerMenu();                // WP-AO-51: secondary-actions dropdown
    wireZoomControls();                 // WP-AO-51: zoom toolbar + keyboard
    wireNodeClickViewer();              // WP-AO-51: per-node CE viewer modal
    applyCanvasPrefs();                 // WP-AO-57: restore palette/trace sizes
    wireSplitters();                    // WP-AO-57: drag handles + persistence
    loadRegistry().then(() => {
      tryAutoLoadDemoWorkflow();        // WP-AO-41: auto-load claims-demo
    });
  });

  // ── WP-AO-57: User-resizable canvas splitters ────────────────────
  const CANVAS_PREFS_KEY = 'wf-canvas-prefs-v1';
  const PALETTE_MIN = 180;
  const PALETTE_MAX = 500;
  const TRACE_MIN = 60;

  function clampPaletteW(w) {
    const max = Math.min(PALETTE_MAX, Math.floor(window.innerWidth * 0.5));
    return Math.max(PALETTE_MIN, Math.min(max, w));
  }
  function clampTraceH(h) {
    const max = Math.floor(window.innerHeight * 0.6);
    return Math.max(TRACE_MIN, Math.min(max, h));
  }

  function loadCanvasPrefs() {
    try {
      const raw = localStorage.getItem(CANVAS_PREFS_KEY);
      if (!raw) return null;
      const p = JSON.parse(raw);
      return {
        paletteW: typeof p.paletteW === 'number' ? p.paletteW : null,
        traceH:   typeof p.traceH   === 'number' ? p.traceH   : null,
      };
    } catch (e) {
      return null;
    }
  }
  function saveCanvasPrefs(prefs) {
    try {
      localStorage.setItem(CANVAS_PREFS_KEY, JSON.stringify(prefs));
    } catch (e) { /* quota etc — non-fatal */ }
  }

  // Apply persisted (or default) sizes to CSS custom properties on :root.
  // Called once during init BEFORE the splitter handlers wire up so the
  // very first paint already has the user's preferred geometry.
  function applyCanvasPrefs() {
    const prefs = loadCanvasPrefs() || {};
    const w = clampPaletteW(prefs.paletteW || 280);
    const h = clampTraceH(prefs.traceH || 180);
    document.documentElement.style.setProperty('--wf-palette-width', w + 'px');
    document.documentElement.style.setProperty('--wf-trace-height',  h + 'px');
  }

  function currentPrefs() {
    const css = getComputedStyle(document.documentElement);
    const w = parseInt(css.getPropertyValue('--wf-palette-width'), 10) || 280;
    const h = parseInt(css.getPropertyValue('--wf-trace-height'),  10) || 180;
    return { paletteW: w, traceH: h };
  }

  function wireSplitters() {
    const hSplitter = document.getElementById('wf-splitter-palette');
    const vSplitter = document.getElementById('wf-splitter-trace');
    if (hSplitter) wireHorizontalSplitter(hSplitter);
    if (vSplitter) wireVerticalSplitter(vSplitter);

    // Re-clamp on window resize so a downsized viewport gracefully shrinks the panes.
    window.addEventListener('resize', () => {
      const cur = currentPrefs();
      document.documentElement.style.setProperty('--wf-palette-width', clampPaletteW(cur.paletteW) + 'px');
      document.documentElement.style.setProperty('--wf-trace-height',  clampTraceH(cur.traceH)    + 'px');
    });

    // WP-AO-57 hotfix — global safety net to clear stuck drag-cursor classes.
    // pointerup/cancel on the splitter element should clear them, but if
    // pointer-capture lapses (browser quirk, focus loss, Drawflow stealing
    // events) the class can persist and leave the cursor stuck in row/col-
    // resize mode across the whole canvas. Belt-and-suspenders cleanup.
    const clearDragClasses = () => {
      document.body.classList.remove('wf-dragging-h', 'wf-dragging-v');
    };
    document.addEventListener('pointerup',     clearDragClasses, true);
    document.addEventListener('pointercancel', clearDragClasses, true);
    document.addEventListener('mouseup',       clearDragClasses, true);
    window.addEventListener('blur',            clearDragClasses);
    // Also: any click in the document that lands while a drag class is
    // still set is interpreted as "drag is definitely over."
    document.addEventListener('click', clearDragClasses, true);
  }

  function wireHorizontalSplitter(el) {
    let dragging = false;
    el.addEventListener('pointerdown', (e) => {
      dragging = true;
      try { el.setPointerCapture(e.pointerId); } catch (_) {}
      el.classList.add('dragging');
      document.body.classList.add('wf-dragging-h');
    });
    el.addEventListener('pointermove', (e) => {
      if (!dragging) return;
      const w = clampPaletteW(e.clientX);
      document.documentElement.style.setProperty('--wf-palette-width', w + 'px');
    });
    const endDrag = (e) => {
      const wasDragging = dragging;
      dragging = false;
      try { el.releasePointerCapture(e.pointerId); } catch (_) {}
      el.classList.remove('dragging');
      document.body.classList.remove('wf-dragging-h');
      if (wasDragging) {
        saveCanvasPrefs(currentPrefs());
        window.dispatchEvent(new Event('resize'));
      }
    };
    el.addEventListener('pointerup',     endDrag);
    el.addEventListener('pointercancel', endDrag);
    el.addEventListener('lostpointercapture', endDrag);
  }

  function wireVerticalSplitter(el) {
    let dragging = false;
    el.addEventListener('pointerdown', (e) => {
      dragging = true;
      try { el.setPointerCapture(e.pointerId); } catch (_) {}
      el.classList.add('dragging');
      document.body.classList.add('wf-dragging-v');
    });
    el.addEventListener('pointermove', (e) => {
      if (!dragging) return;
      const h = clampTraceH(window.innerHeight - e.clientY);
      document.documentElement.style.setProperty('--wf-trace-height', h + 'px');
    });
    const endDrag = (e) => {
      const wasDragging = dragging;
      dragging = false;
      try { el.releasePointerCapture(e.pointerId); } catch (_) {}
      el.classList.remove('dragging');
      document.body.classList.remove('wf-dragging-v');
      if (wasDragging) {
        saveCanvasPrefs(currentPrefs());
        window.dispatchEvent(new Event('resize'));
      }
    };
    el.addEventListener('pointerup',     endDrag);
    el.addEventListener('pointercancel', endDrag);
    el.addEventListener('lostpointercapture', endDrag);
  }

  // ── WP-AO-51: Hamburger menu (Load/Save + audit toggles) ─────────
  function wireHamburgerMenu() {
    const btn = document.getElementById('wf-menu-btn');
    const dd  = document.getElementById('wf-menu-dropdown');
    if (!btn || !dd) return;
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      dd.classList.toggle('hidden');
    });
    // Click outside to close
    document.addEventListener('click', (e) => {
      if (!dd.classList.contains('hidden') &&
          !dd.contains(e.target) && e.target !== btn) {
        dd.classList.add('hidden');
      }
    });
    // Esc to close
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && !dd.classList.contains('hidden')) {
        dd.classList.add('hidden');
      }
    });
  }

  // ── WP-AO-51: Zoom controls — toolbar buttons + keyboard +/-/0 ──
  function wireZoomControls() {
    const inBtn   = document.getElementById('wf-zoom-in');
    const outBtn  = document.getElementById('wf-zoom-out');
    const resetBtn = document.getElementById('wf-zoom-reset');
    if (inBtn)   inBtn.addEventListener('click',   () => editor?.zoom_in());
    if (outBtn)  outBtn.addEventListener('click',  () => editor?.zoom_out());
    if (resetBtn) resetBtn.addEventListener('click', () => editor?.zoom_reset());
    // Keyboard shortcuts — only when the canvas-related fields aren't focused.
    document.addEventListener('keydown', (e) => {
      const target = e.target;
      const isInput = target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable);
      if (isInput) return;
      if (e.key === '+' || (e.key === '=' && e.shiftKey === false && e.metaKey === false)) {
        e.preventDefault();
        editor?.zoom_in();
      } else if (e.key === '-' || e.key === '_') {
        e.preventDefault();
        editor?.zoom_out();
      } else if (e.key === '0') {
        e.preventDefault();
        editor?.zoom_reset();
      }
    });
  }

  // ── WP-AO-51 (revised): Per-node CE viewer popup — strip-only ────
  // Earlier version hooked editor.on('nodeSelected'), which fired on
  // every click including drag-start → modal popped up unwanted when
  // user just wanted to move a node. Replaced with a dedicated ".ce-
  // card-viewer-strip" element rendered inside each node's HTML;
  // global click delegation opens the modal only when THAT strip is
  // clicked. Rest of the node remains drag-targetable as usual.
  function wireNodeClickViewer() {
    // Global delegate — listens on the canvas container so it catches
    // strip clicks regardless of which node was clicked.
    const canvas = document.querySelector('.wf-canvas-wrap') || document.body;
    canvas.addEventListener('click', (e) => {
      const strip = e.target.closest('.ce-card-viewer-strip');
      if (!strip) return;
      e.stopPropagation();   // don't let Drawflow treat this as node-select
      const slug = strip.getAttribute('data-ce-slug');
      if (!slug) return;
      const meta = ceRegistry.find(
        (c) => `${c.workflow_slug}/${c.stage_slug}` === slug
      );
      if (!meta) return;
      openNodeViewer(meta);
    });
    document.getElementById('wf-ce-modal-close')?.addEventListener('click', closeNodeViewer);
    document.getElementById('wf-ce-modal')?.addEventListener('click', (e) => {
      // Click on the backdrop (not the card) closes
      if (e.target.id === 'wf-ce-modal') closeNodeViewer();
    });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        const modal = document.getElementById('wf-ce-modal');
        if (modal && !modal.classList.contains('hidden')) closeNodeViewer();
      }
    });
  }

  async function openNodeViewer(meta) {
    const modal = document.getElementById('wf-ce-modal');
    const frame = document.getElementById('wf-ce-modal-frame');
    if (!modal || !frame) return;

    const wf = meta.workflow_slug;
    const stage = meta.stage_slug;
    const apiPath = `/ce/${wf}/${stage}/`;

    // Fetch the full CESpec to get sample_i + contract title (the registry
    // listing endpoint returns a light envelope; the contract endpoint
    // returns the full spec including sample input).
    let sample = '';
    let title = meta.contract_title || `${wf}/${stage}`;
    try {
      const spec = await fetchContract(`${wf}/${stage}`);
      if (spec && spec.sample_i) sample = spec.sample_i;
      if (spec && spec.contract_title) title = spec.contract_title;
    } catch (e) { /* harmless — viewer falls back to {} */ }

    const params = new URLSearchParams();
    params.set('api', apiPath);
    if (sample) params.set('sample', sample);
    if (title) params.set('title', title);
    params.set('key', authKey);
    const url = `/ce-viewer?${params.toString()}`;

    frame.src = url;
    modal.classList.remove('hidden');
  }

  function closeNodeViewer() {
    const modal = document.getElementById('wf-ce-modal');
    const frame = document.getElementById('wf-ce-modal-frame');
    if (modal) modal.classList.add('hidden');
    if (frame) frame.src = 'about:blank';   // stops any in-flight request
  }

  // ── WP-AO-49 Unit 3: CE Palette reload ───────────────────────────
  // Two trigger paths:
  //   (a) Manual click on the ↻ button in the palette header
  //   (b) Parent window postMessage({type: 'reload-registry'}) — fired by
  //       crafter-app.js when the user switches TO the Workflow Canvas tab,
  //       so freshly-created CEs in the Crafter tab appear without a full
  //       page reload.
  function wirePaletteReload() {
    const btn = document.getElementById('wf-palette-reload');
    if (btn) {
      btn.addEventListener('click', async () => {
        btn.classList.add('spinning');
        try { await loadRegistry(); }
        finally { setTimeout(() => btn.classList.remove('spinning'), 600); }
      });
    }
    window.addEventListener('message', (evt) => {
      if (!evt.data || evt.data.type !== 'reload-registry') return;
      loadRegistry().catch(() => {});
    });
  }

  // ── WP-01 U5: trust-gate chip click → layer popup modal ─────────
  // Chip click reads data-layer + finds the drawflow node ID by walking up
  // to the parent `.drawflow-node`. Looks up latestLayerResults[nodeID][layer]
  // and routes to openLayerModal (DENY/LOG) or toast (ALLOW/no-data).
  // Delegation attached to document (not #drawflow) so Drawflow's pointer
  // handlers can't swallow the chip click before we see it.
  function wireLayerChipClicks() {
    document.addEventListener('click', (e) => {
      const chip = e.target.closest('.wf-trust-chip');
      if (!chip) return;
      e.preventDefault();
      e.stopPropagation();
      const layer = chip.dataset.layer;
      const ceSlug = chip.dataset.ceSlug || '';
      const nodeEl = chip.closest('.drawflow-node');
      if (!nodeEl) {
        toast(`${layer}: no run data yet — click ▶ Run first`, 'warn', 2400);
        return;
      }
      const nodeID = parseInt(nodeEl.id.replace(/^node-/, ''), 10);
      const rec = latestLayerResults.get(nodeID);
      const evt = rec ? rec[layer] : null;
      if (!evt) {
        toast(`${layer}: no run data for this node — click ▶ Run first`, 'warn', 2400);
        return;
      }
      // ALLOW verdicts: tiny toast, no modal (per David spec — popup only on issue).
      const v = (evt.verdict || '').toUpperCase();
      if (v === 'ALLOW' || v === 'SUCCESS' || v === 'SKIPPED') {
        toast(`${layer} ${v} (no issues) — ${ceSlug}`, 'ok', 2400);
        return;
      }
      openLayerModal(layer, ceSlug, evt);
    }, true); // capture phase — beats Drawflow's bubble-phase node handlers

    // Modal close handlers (Esc + close button + click-outside).
    const closeBtn = document.getElementById('wf-layer-modal-close');
    if (closeBtn) closeBtn.addEventListener('click', closeLayerModal);
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') closeLayerModal();
    });
    const modal = document.getElementById('wf-layer-modal');
    if (modal) modal.addEventListener('click', (e) => {
      if (e.target === modal) closeLayerModal();
    });
  }

  function openLayerModal(layer, ceSlug, evt) {
    const modal = document.getElementById('wf-layer-modal');
    if (!modal) return;
    const title = document.getElementById('wf-layer-modal-title');
    const summary = document.getElementById('wf-layer-modal-summary');
    const payloadEl = document.getElementById('wf-layer-modal-payload');
    if (!title || !summary || !payloadEl) return;

    const verdict = (evt.verdict || 'UNKNOWN').toUpperCase();
    const verdictClass = (verdict === 'DENY' || verdict === 'FAILURE')
      ? 'verdict-deny'
      : (verdict === 'LOG' ? 'verdict-log' : 'verdict-allow');

    title.className = 'wf-layer-modal-title ' + verdictClass;
    title.textContent = `${layer} — ${verdict}  ·  ${ceSlug}`;

    // Build summary fields. L0/L3 carry risk_score / matched_rule / flags;
    // L1/L2 carry score / reasons / meta. Meta is a JSON.RawMessage from Go;
    // for L0/L3 that's where risk_score/matched_rule live.
    let meta = {};
    try { if (evt.meta) meta = (typeof evt.meta === 'string' ? JSON.parse(evt.meta) : evt.meta); }
    catch (e) { meta = {}; }

    let rows = '';
    rows += summaryRow('Verdict', verdict);
    if (layer === 'L0' || layer === 'L3') {
      const rs = meta.risk_score ?? evt.risk_score ?? '—';
      const mr = meta.matched_rule || evt.matched_rule || '—';
      const dm = meta.deny_message || evt.deny_message || '—';
      const fl = (meta.flags || evt.flags || []).join(', ') || '—';
      rows += summaryRow('Risk score', String(rs));
      rows += summaryRow('Matched rule', mr);
      rows += summaryRow('Flags', fl);
      rows += summaryRow('Deny message', dm);
    } else {
      const sc = evt.score ?? meta.score ?? '—';
      const rsn = (evt.reasons || meta.reasons || []).join(' · ') || '—';
      rows += summaryRow('Score', String(sc));
      rows += summaryRow('Reasons', rsn);
    }
    if (evt.latency_ms) rows += summaryRow('Latency', evt.latency_ms + ' ms');
    if (evt.timestamp) rows += summaryRow('Timestamp', evt.timestamp);
    summary.innerHTML = rows;

    payloadEl.textContent = JSON.stringify({ event: evt, meta }, null, 2);
    modal.classList.remove('hidden');
  }

  function summaryRow(label, value) {
    return `<div class="wf-layer-modal-row">
      <span class="wf-layer-modal-label">${escapeHTML(label)}</span>
      <span class="wf-layer-modal-value">${escapeHTML(String(value))}</span>
    </div>`;
  }

  function closeLayerModal() {
    const modal = document.getElementById('wf-layer-modal');
    if (modal) modal.classList.add('hidden');
  }

  // ── WP-01: 📋 Audit log button + modal ──────────────────────────
  // Fetches /api/audit-log?workflow_slug=<current> and renders a table of
  // every L0/L1/L2/L3 verdict. Demo-ready surface for the "regulator-readable
  // evidence" claim on slide 3 / 4 of the pitch.
  function wireAuditLogModal() {
    const btn      = document.getElementById('wf-audit-btn');
    const modal    = document.getElementById('wf-audit-modal');
    const closeBtn = document.getElementById('wf-audit-modal-close');
    if (!btn || !modal) return;
    btn.addEventListener('click', () => openAuditLog());
    if (closeBtn) closeBtn.addEventListener('click', closeAuditLog);
    document.getElementById('wf-audit-refresh')?.addEventListener('click', () => loadAuditLog());
    document.getElementById('wf-audit-layer') ?.addEventListener('change', () => loadAuditLog());
    document.getElementById('wf-audit-verdict')?.addEventListener('change', () => loadAuditLog());
    document.getElementById('wf-audit-limit')  ?.addEventListener('change', () => loadAuditLog());
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && !modal.classList.contains('hidden')) closeAuditLog();
    });
    modal.addEventListener('click', (e) => { if (e.target === modal) closeAuditLog(); });
  }
  function openAuditLog() {
    const modal = document.getElementById('wf-audit-modal');
    if (!modal) return;
    modal.classList.remove('hidden');
    loadAuditLog();
  }
  function closeAuditLog() {
    document.getElementById('wf-audit-modal')?.classList.add('hidden');
  }
  async function loadAuditLog() {
    const wfSlug  = ($('#wf-slug-input')?.value || '').trim() || 'untitled';
    const layer   = document.getElementById('wf-audit-layer')?.value   || 'all';
    const verdict = document.getElementById('wf-audit-verdict')?.value || 'all';
    const limit   = document.getElementById('wf-audit-limit')?.value   || '200';
    const status  = document.getElementById('wf-audit-status');
    const tbody   = document.getElementById('wf-audit-tbody');
    const wfEl    = document.getElementById('wf-audit-modal-wf');
    const sumEl   = document.getElementById('wf-audit-summary');
    if (wfEl) wfEl.textContent = `· ${wfSlug}`;
    if (status) status.textContent = 'loading…';
    if (tbody) tbody.innerHTML = '<tr><td colspan="7" class="wf-audit-empty">Loading…</td></tr>';
    const params = new URLSearchParams({ workflow_slug: wfSlug, layer, verdict, limit });
    try {
      const res = await fetch('/api/audit-log?' + params.toString(), {
        headers: { 'X-Access-Key': authKey }
      });
      const data = await res.json();
      if (!res.ok) {
        if (status) status.textContent = 'error: ' + (data.detail || res.status);
        if (tbody) tbody.innerHTML = `<tr><td colspan="7" class="wf-audit-empty">${escapeHTML(data.detail || ('HTTP ' + res.status))}</td></tr>`;
        return;
      }
      // Render summary chips
      if (sumEl) sumEl.innerHTML = renderAuditSummary(data.summary || {});
      // Render rows
      if (tbody) {
        if (!data.rows || data.rows.length === 0) {
          tbody.innerHTML = '<tr><td colspan="7" class="wf-audit-empty">No matching rows. Run the workflow first.</td></tr>';
        } else {
          tbody.innerHTML = data.rows.map(renderAuditRow).join('');
        }
      }
      if (status) status.textContent = `${data.count} rows`;
    } catch (e) {
      if (status) status.textContent = 'error: ' + e.message;
      if (tbody) tbody.innerHTML = `<tr><td colspan="7" class="wf-audit-empty">${escapeHTML(e.message)}</td></tr>`;
    }
  }
  function renderAuditSummary(summary) {
    const order = ['L0', 'L1', 'L2', 'L3'];
    const parts = [];
    for (const layer of order) {
      const rec = summary[layer];
      if (!rec) continue;
      const sub = Object.entries(rec)
        .map(([v, n]) => `<span class="wf-audit-pill v-${escAttr(v).toLowerCase()}">${escapeHTML(v)} ${n}</span>`)
        .join(' ');
      parts.push(`<span class="wf-audit-pill-group"><b>${layer}</b> ${sub}</span>`);
    }
    if (parts.length === 0) return '<span class="wf-audit-empty">No audit rows yet — click ▶ Run.</span>';
    return parts.join('');
  }
  function renderAuditRow(r) {
    const ts = (r.ts || '').replace('T', ' ').slice(0, 23);
    const vCls = ('v-' + String(r.verdict || '').toLowerCase());
    const risk = (typeof r.risk_score === 'number' ? r.risk_score.toFixed(2) : '—');
    return `<tr>
      <td class="wf-audit-ts">${escapeHTML(ts)}</td>
      <td>${escapeHTML(r.ce_slug || '—')}</td>
      <td class="wf-audit-layer wf-audit-layer-${escAttr(r.layer)}">${escapeHTML(r.layer)}</td>
      <td class="wf-audit-verdict ${vCls}">${escapeHTML(r.verdict)}</td>
      <td>${risk}</td>
      <td>${escapeHTML(r.matched_rule || '—')}</td>
      <td class="wf-audit-deny">${escapeHTML(r.deny_message || '')}</td>
    </tr>`;
  }
  function escAttr(s) { return String(s || '').replace(/[^a-z0-9]/gi, ''); }

  // ── ⚡ Load demo project — one-click 6-CE bootstrap ──────────────
  // POSTs /api/crafter/bootstrap-demo (server seeds the health-insurance-claim
  // CEs from embedded demo-assets), then reloads the palette and attempts the
  // claims-demo workflow auto-load so the canvas populates without a refresh.
  function wireLoadDemoBtn() {
    const btn = document.getElementById('btn-load-demo');
    if (!btn) return;
    const original = btn.textContent;
    btn.addEventListener('click', async () => {
      btn.disabled = true;
      btn.textContent = '⏳ Loading demo…';
      try {
        const r = await fetch('/api/crafter/bootstrap-demo', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-Access-Key': authKey },
          // force=true so existing CEs get re-seeded with the latest schema
          // (e.g. WP-01 U6 added trust_gate_l0 / trust_gate_l3 defaults — old
          // CEs on disk would otherwise stay at trust_gate_*=0 = layer skipped).
          body: '{"force":true}'
        });
        const data = await r.json();
        if (!r.ok) throw new Error(data.error || `HTTP ${r.status}`);
        const ceCount = data.ce_count || 0;
        const skipped = (data.skipped || []).length;
        const errs = (data.errors || []).length;
        btn.textContent = `✓ ${ceCount} CEs ready` + (skipped ? ` (${skipped} skipped)` : '') + (errs ? ` [${errs} errors]` : '');
        await loadRegistry();
        await tryAutoLoadDemoWorkflow();
      } catch (e) {
        toast('Demo bootstrap failed: ' + e.message, 'err', 4000);
        btn.textContent = original;
      } finally {
        setTimeout(() => { btn.disabled = false; btn.textContent = original; }, 4000);
      }
    });
  }

  // ── WP-AO-39: Deploy state machine + collision modal ─────────────
  // States: UNTESTED | TESTING | TESTED_SUCCESS | TESTED_FAIL | DEPLOYING | DEPLOYED
  // Reset triggers (back to UNTESTED): node add/remove, connection add/remove,
  // node data change (CE binding swap), Load.
  // TESTED_SUCCESS requires: SSE 'complete' event with final_status === 'SUCCESS'
  // AND no prior 'halt' phase events in the run.
  let deployPhase = 'UNTESTED';
  let runHadHalt = false;

  function setDeployPhase(phase) {
    deployPhase = phase;
    const btn = document.getElementById('wf-deploy-btn');
    if (!btn) return;
    btn.classList.remove('phase-untested', 'phase-tested-success', 'phase-tested-fail', 'phase-deploying', 'phase-deployed');
    btn.classList.add('phase-' + phase.toLowerCase().replace('_', '-'));
    switch (phase) {
      case 'UNTESTED':
        btn.disabled = true;
        btn.textContent = 'Deploy now';
        btn.title = 'Run a test first to enable Deploy';
        break;
      case 'TESTING':
        btn.disabled = true;
        btn.textContent = 'Testing…';
        btn.title = 'Test run in flight';
        break;
      case 'TESTED_SUCCESS':
        btn.disabled = false;
        btn.textContent = '▶ Deploy now';
        btn.title = 'Test passed — click to publish a public URL';
        break;
      case 'TESTED_FAIL':
        btn.disabled = true;
        btn.textContent = 'Test failed';
        btn.title = 'Test run halted — fix and retry before deploying';
        break;
      case 'DEPLOYING':
        btn.disabled = true;
        btn.textContent = 'Deploying…';
        btn.title = 'Publishing workflow…';
        break;
      case 'DEPLOYED':
        btn.disabled = false;
        btn.textContent = '✓ Re-deploy';
        btn.title = 'Deployed. Click to push a new version.';
        break;
    }
  }

  // Hook Drawflow structural-change events → reset to UNTESTED.
  function wireDeployResetTriggers() {
    if (!editor) return;
    const reset = () => { if (deployPhase !== 'DEPLOYING') setDeployPhase('UNTESTED'); };
    editor.on('nodeCreated',       reset);
    editor.on('nodeRemoved',       reset);
    editor.on('connectionCreated', reset);
    editor.on('connectionRemoved', reset);
    editor.on('nodeDataChanged',   reset);
  }

  // Wrap doRun & SSE complete by tracking via wf-run-btn click + listening on
  // the shared activeEventSource pattern. Run-button click → TESTING (additive
  // listener fires after the doRun listener); SSE 'complete' is detected by
  // polling a marker that handleRunEvent + showFinalBanner side-effect into.
  // Cleanest: wrap doRun & the complete handler by adding listener AFTER
  // initialization.
  document.addEventListener('DOMContentLoaded', () => {
    const runBtn = document.getElementById('wf-run-btn');
    if (runBtn) {
      runBtn.addEventListener('click', () => {
        runHadHalt = false;
        setDeployPhase('TESTING');
      });
    }
    wireDeployResetTriggers();
    wireDeployButton();
    setDeployPhase('UNTESTED');
  });

  // Patch handleRunEvent's effects by also tracking halt phases.
  // We can't reach into the IIFE-scoped handleRunEvent from here without
  // refactor, so instead we hook the EventSource readyState via a periodic
  // check on the trace banner that handleRunEvent populates.
  // Simpler: poll for the run-state outcome by hooking the same SSE stream
  // ourselves and listening for halt/complete. The server accepts multiple
  // SSE subscribers on the same run_id? Actually the existing handler reads
  // from a single channel; second subscriber would race. Better approach:
  // observe the DOM state that handleRunEvent already updates.
  const TRACE_BANNER_SELECTOR = '#wf-trace-banner';
  function observeRunCompletion() {
    const banner = document.querySelector(TRACE_BANNER_SELECTOR);
    if (!banner) return;
    // Banner gets a class added when run completes; we observe that.
    const obs = new MutationObserver(() => {
      if (banner.classList.contains('hidden')) return;
      const txt = (banner.textContent || '').toUpperCase();
      if (txt.includes('SUCCESS')) {
        setDeployPhase('TESTED_SUCCESS');
      } else if (txt.includes('HALTED') || txt.includes('ERROR') || txt.includes('FAIL')) {
        setDeployPhase('TESTED_FAIL');
      }
    });
    obs.observe(banner, { attributes: true, attributeFilter: ['class'], childList: true, subtree: true });
  }
  document.addEventListener('DOMContentLoaded', observeRunCompletion);

  // Deploy button + collision modal wiring
  function wireDeployButton() {
    const btn = document.getElementById('wf-deploy-btn');
    if (!btn) return;
    btn.addEventListener('click', () => {
      if (deployPhase !== 'TESTED_SUCCESS' && deployPhase !== 'DEPLOYED') return;
      doDeploy(false, null);
    });
    document.getElementById('wf-deploy-modal-overwrite')?.addEventListener('click', () => {
      hideCollisionModal();
      doDeploy(true, null);
    });
    document.getElementById('wf-deploy-modal-alt')?.addEventListener('click', () => {
      const altSlug = document.getElementById('wf-deploy-modal-alt').dataset.altSlug;
      hideCollisionModal();
      doDeploy(false, altSlug);
    });
    document.getElementById('wf-deploy-modal-cancel')?.addEventListener('click', () => {
      hideCollisionModal();
      setDeployPhase('TESTED_SUCCESS');  // back to ready
    });
    document.getElementById('wf-deploy-success-close')?.addEventListener('click', () => {
      document.getElementById('wf-deploy-success')?.classList.add('hidden');
    });
    document.getElementById('wf-deploy-success-copy')?.addEventListener('click', () => {
      const url = document.getElementById('wf-deploy-success-url')?.textContent || '';
      navigator.clipboard?.writeText(url).then(() => toast('URL copied', 'ok'), () => toast('Clipboard blocked', 'danger'));
    });
    document.getElementById('wf-deploy-success-open')?.addEventListener('click', () => {
      const url = document.getElementById('wf-deploy-success-url')?.textContent || '';
      if (url) window.open(url, '_blank', 'noopener');
    });
  }

  async function doDeploy(force, slugOverride) {
    setDeployPhase('DEPLOYING');
    const slug = slugOverride || ($('#wf-slug-input')?.value || '').trim() || 'untitled';
    const title = ($('#wf-title-input')?.value || '').trim() || slug;
    if (slugOverride) {
      const slugInput = $('#wf-slug-input');
      if (slugInput) slugInput.value = slugOverride;
    }
    const drawflowExport = editor.export();
    const wf = drawflowToCanonical(drawflowExport, slug, title);
    if (wf.nodes.length === 0) {
      toast('Empty canvas — nothing to deploy', 'warn');
      setDeployPhase('TESTED_SUCCESS');
      return;
    }
    try {
      const res = await fetch('/api/workflow/deploy', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Access-Key': authKey },
        body: JSON.stringify({ slug, workflow: wf, force }),
      });
      if (res.status === 409) {
        const body = await res.json();
        showCollisionModal(body.existing_slug, body.existing_deployed_at, body.suggested_alternative_slug);
        setDeployPhase('TESTED_SUCCESS');  // back to ready; user picks in modal
        return;
      }
      if (!res.ok) {
        const txt = await res.text();
        toast(`Deploy failed (${res.status}): ${txt}`, 'danger', 5000);
        setDeployPhase('TESTED_SUCCESS');
        return;
      }
      const data = await res.json();
      showDeploySuccess(data.url, data.slug);
      setDeployPhase('DEPLOYED');
    } catch (e) {
      toast(`Deploy network error: ${e.message}`, 'danger');
      setDeployPhase('TESTED_SUCCESS');
    }
  }

  function showCollisionModal(existingSlug, deployedAt, suggestedAlt) {
    const modal = document.getElementById('wf-deploy-modal');
    if (!modal) return;
    const body = document.getElementById('wf-deploy-modal-body');
    const altBtn = document.getElementById('wf-deploy-modal-alt');
    if (body) {
      body.innerHTML = `Workflow <code>${escapeHTML(existingSlug)}</code> already deployed${deployedAt ? ' (' + escapeHTML(deployedAt) + ')' : ''}. ` +
                       `Choose: <strong>overwrite</strong> the existing one, or <strong>deploy as a new slug</strong> <code>${escapeHTML(suggestedAlt)}</code>.`;
    }
    if (altBtn) {
      altBtn.textContent = `Deploy as "${suggestedAlt}"`;
      altBtn.dataset.altSlug = suggestedAlt;
    }
    modal.classList.remove('hidden');
  }
  function hideCollisionModal() {
    document.getElementById('wf-deploy-modal')?.classList.add('hidden');
  }

  function showDeploySuccess(url, slug) {
    const card = document.getElementById('wf-deploy-success');
    const linkEl = document.getElementById('wf-deploy-success-url');
    if (card && linkEl) {
      linkEl.textContent = url;
      linkEl.href = url;
      card.classList.remove('hidden');
    }
    toast(`Deployed: ${slug}`, 'ok', 4000);
  }

  // Export hooks so Unit 6 can extend without copy-pasting state
  window.WF = {
    get editor()       { return editor; },
    get nodeCEMeta()   { return nodeCEMeta; },
    get ceRegistry()   { return ceRegistry; },
    get ceContractCache() { return ceContractCache; },
    get authKey()      { return authKey; },
    toast,
    escapeHTML,
    ceCardHTML,
    drawflowToCanonical,
    canonicalToDrawflow,
    fetchContract,
    compareSchemas,
  };
})();
