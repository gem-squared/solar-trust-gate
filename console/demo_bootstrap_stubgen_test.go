//go:build demobootstrap

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestGenHealthClaimStubs is a one-shot helper invoked by the demo
// bootstrap script. Runs via:
//
//   HEALTH_CLAIM_DIR=<src> HEALTH_CLAIM_OUT=<dst> \
//       go test -tags demobootstrap -run TestGenHealthClaimStubs ./console/
//
// Parses the 6 health-insurance-claim contract markdowns into clean
// CESpec JSON stubs (empty SampleI + ReferenceData — the /data-synthesize
// call later populates them) and writes one .json per CE to HEALTH_CLAIM_OUT.
func TestGenHealthClaimStubs(t *testing.T) {
	src := os.Getenv("HEALTH_CLAIM_DIR")
	if src == "" {
		src = "../Docs/Workflow-candidates/health-insurance-claim"
	}
	dst := os.Getenv("HEALTH_CLAIM_OUT")
	if dst == "" {
		t.Fatal("HEALTH_CLAIM_OUT env var must be set to the target directory")
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", dst, err)
	}

	files := []string{
		"claim-01-intake.md",
		"claim-02-policy-verification.md",
		"claim-03-eligibility-check.md",
		"claim-04-medical-review.md",
		"claim-05-adjudication.md",
		"claim-06-disbursement.md",
	}
	for _, name := range files {
		path := filepath.Join(src, name)
		spec, err := parseContractFile(path)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		// Bootstrap baseline: empty SampleI + ReferenceData so the
		// follow-up /data-synthesize call has a clean target.
		spec.SampleI = ""
		spec.ReferenceData = nil

		out, _ := json.MarshalIndent(spec, "", "  ")
		dstFile := filepath.Join(dst, spec.StageSlug+".json")
		if err := os.WriteFile(dstFile, out, 0o644); err != nil {
			t.Fatalf("write %s: %v", dstFile, err)
		}
		fmt.Printf("✓ %s → %s (%d bytes)\n", name, dstFile, len(out))
	}
}
