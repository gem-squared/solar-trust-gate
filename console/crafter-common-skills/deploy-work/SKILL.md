---
name: deploy-work
description: >
  (What) Promote the active project's output to a publicly reachable URL.
  (When) Final stage of the canonical pipeline plan-work → proceed-work → verify-work → deploy-work,
  fired after the user confirms (or 60s idle elapses) at the post-proceed-work checkpoint.
  (Why) Closes the I→P→E→V→D pipeline with a concrete "now it's live" artifact for judges + auditors.
  (How) Walk the project's output/{slug}/ tree, build the public URL host/p/{slug}/, return list of
  files + URL. For CE-only flows whose output dir is empty (CE is live the moment /create-ce returns),
  return a no-op success with a note pointing back to the existing CE viewer.
argument-hint: "[no args — uses active project from session]"
metadata:
  author: David Seo of GEM².AI
  version: 1.0.0
  introduced_by: WP-AO-35
---

(* TPMN SKILL — deploy-work *)
(* WP-AO-35 Unit 1 — canonical pipeline final stage *)

(* === Layers === *)
L0 ≜ "filesystem walk over {ProjectDir}/output/{slug}/ — no MCP, no LLM"

(* === Input === *)
A ≜ [
  session: SessionData,        (* active session — must have ActiveProject set *)
  project_slug: 𝕊?             (* override; ⊥ = derive from session.ActiveProject *)
]

(* === Output === *)
B ≜ [
  deployed_url:   𝕊,            (* https://ai-olympic.gemsquared.ai/p/{slug}/ *)
  files:          Seq(𝕊),       (* relative paths under output/{slug}/ *)
  file_count:     ℕ,
  output_kind:    {project, ce-only, empty},
                                (* "project"  → output/ has files served at /p/{slug}/ *)
                                (* "ce-only"  → output/ empty, CE registered at /ce/.../ — directs back to viewer *)
                                (* "empty"    → no work produced; surface a graceful "nothing to deploy" *)
  immediately_reachable: 𝔹      (* ⊤ if URL is live (Caddy fronts via SSL) *)
]

(* === Precondition === *)
P ≜ session.ActiveProject ≠ ⊥
    ∧ {baseDir}/.gem-squared/workspace/{ActiveProject}/ exists as directory

(* === Transform === *)
F ≜ <<
  1. Resolve project_slug:
       slug = project_slug ?: session.ActiveProject
       projectDir = filepath.Join(baseDir, ".gem-squared", "workspace", slug)

  2. Walk projectDir/output/{slug}/ recursively:
       Collect every file's relative path under output/{slug}/.

  3. Determine output_kind:
       IF len(files) > 0           → output_kind = "project",  deployed_url = host/p/{slug}/
       ELSE IF CE registry has an entry whose source_file is under projectDir/uploaded_files/
                                   → output_kind = "ce-only",  deployed_url = the viewer URL for that CE
                                     (most recent CE if multiple)
       ELSE                        → output_kind = "empty",    deployed_url = ⊥

  4. Compose response markdown:
       output_kind = "project":
         "## Deployed!\n\n**[▶ Open Deployed Site](deployed_url)**\n\n**Files:** N\n\nYour project is now live."
       output_kind = "ce-only":
         "## CE Deploy complete\n\nThe Contract-Executor is already live at the registered endpoint.
          ▶ Use the viewer button from /create-ce to test it."
       output_kind = "empty":
         "## Nothing to deploy\n\nNo output artifacts found under output/{slug}/. If this was a CE-only
          flow, the CE is already live via /create-ce — no separate deploy needed."

  5. Return SkillExecResult{Skill="deploy-work", OutputB=composed markdown, Duration=elapsed}.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ NEVER write to disk in F — read-only walk over filesystem,
  ⊢ NEVER invoke /create-ce or /create-project — those are separate skills,
  ⊢ The "ce-only" branch reads from the CE registry only — does not modify it,
  ⊢ Production host is `ceProductionHost` (https://ai-olympic.gemsquared.ai) —
     the same constant used by /create-ce so deploy URLs match the viewer URLs
]

(* === Invariant === *)
INV ≜ [
  ⊢ The pipeline stage 'D' (Deploy) of I→P→E→V→D lights up when this skill SUCCEEDS,
    regardless of which output_kind branch fires,
  ⊢ For CE-only flows, the user's expectation "the CE is deployed" is already met by /create-ce;
    this skill confirms that rather than duplicating the deployment work,
  ⊢ Markdown output uses the [[CE_VIEWER_BUTTON|url]] token format for the project-deploy branch
    so the cream-pulsing pill renders in chat (consistent with /create-ce success path),
  ⊢ This skill is the LAST step of the canonical pipeline — no further auto-routing fires after it
]

(* === Routing === *)
G(deploy-work, complete) →
  emit chat-summary "pipeline complete" (no further skill chain)
