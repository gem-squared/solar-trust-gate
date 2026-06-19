package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func loadContractFromMarkdown(path string) (*ContractDef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read contract: %w", err)
	}
	content := string(data)

	base := filepath.Base(path)
	id := strings.TrimSuffix(base, filepath.Ext(base))

	titleRe := regexp.MustCompile(`^#\s+(.+)`)
	name := id
	if m := titleRe.FindStringSubmatch(content); len(m) > 1 {
		name = strings.TrimSpace(m[1])
	}

	contract := &ContractDef{
		ID:              id,
		Name:            name,
		InputSchema:     extractSection(content, "## A:"),
		ProcessingLogic: extractSection(content, "## F:"),
		OutputSchema:    extractSection(content, "## B:"),
		Postconditions:  extractSection(content, "## P:"),
	}

	return contract, nil
}

func extractSection(content, header string) string {
	idx := strings.Index(content, header)
	if idx == -1 {
		return ""
	}
	start := idx + len(header)
	// skip to end of the header line
	if nl := strings.IndexByte(content[start:], '\n'); nl >= 0 {
		start += nl + 1
	}
	// find the next ## header
	rest := content[start:]
	nextHeader := regexp.MustCompile(`\n## `)
	if loc := nextHeader.FindStringIndex(rest); loc != nil {
		return strings.TrimSpace(rest[:loc[0]])
	}
	return strings.TrimSpace(rest)
}

func loadSyntheticData(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read synthetic data dir: %w", err)
	}

	data := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			log.Printf("[CONTRACT-LOADER] skip %s: %v", e.Name(), err)
			continue
		}
		data[e.Name()] = string(content)
	}
	return data, nil
}

func loadContractPipeline(contractsDir string) ([]*ContractDef, error) {
	ordered := []string{
		"claim-01-intake.md",
		"claim-02-policy-verification.md",
		"claim-03-eligibility-check.md",
		"claim-04-medical-review.md",
		"claim-05-adjudication.md",
		"claim-06-disbursement.md",
	}

	var contracts []*ContractDef
	for _, name := range ordered {
		path := filepath.Join(contractsDir, name)
		c, err := loadContractFromMarkdown(path)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", name, err)
		}
		contracts = append(contracts, c)
		log.Printf("[CONTRACT-LOADER] loaded %s: %s (A: %d chars, F: %d chars, B: %d chars, P: %d chars)",
			c.ID, c.Name, len(c.InputSchema), len(c.ProcessingLogic), len(c.OutputSchema), len(c.Postconditions))
	}
	return contracts, nil
}

// dbContextForContract returns the relevant synthetic DB content for a given contract
func dbContextForContract(contractID string, syntheticData map[string]string) string {
	relevantDBs := map[string][]string{
		"claim-01-intake":              {"db_policies.json"},
		"claim-02-policy-verification": {"db_policies.json"},
		"claim-03-eligibility-check":   {"db_plan_benefits.json", "db_pre_auth_and_utilisation.json"},
		"claim-04-medical-review":      {"db_providers.json", "db_physicians.json", "db_pre_auth_and_utilisation.json"},
		"claim-05-adjudication":        {"db_plan_benefits.json", "db_pre_auth_and_utilisation.json"},
		"claim-06-disbursement":        {"db_policies.json", "db_plan_benefits.json"},
	}

	dbs, ok := relevantDBs[contractID]
	if !ok {
		return ""
	}

	var parts []string
	for _, dbName := range dbs {
		if content, found := syntheticData[dbName]; found {
			parts = append(parts, fmt.Sprintf("### %s\n```json\n%s\n```", dbName, content))
		}
	}
	return strings.Join(parts, "\n\n")
}
