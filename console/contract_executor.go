package main

import (
	"fmt"
	"strings"
)

type ContractDef struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	InputSchema     string `json:"input_schema"`
	ProcessingLogic string `json:"processing_logic"`
	OutputSchema    string `json:"output_schema"`
	Postconditions  string `json:"postconditions"`
}

func assembleContractPrompt(contract ContractDef, inputData, dbContext string) (systemPrompt, userPrompt string) {
	systemPrompt = fmt.Sprintf(`You are a TPMN contract executor. You receive a contract definition and input data.
Your job: apply the processing logic (F) to the input (A) and produce output matching schema (B).

## Contract: %s — %s

## Rules
1. Follow the processing logic EXACTLY — no improvisation, no shortcuts.
2. Output ONLY valid JSON matching the B (output) schema.
3. Every field in the output schema must be present.
4. For calculations, apply the exact formulas specified — the output will be verified against postconditions.
5. For lookups, use ONLY the reference database provided — do not invent data.
6. No markdown, no explanation, no code fences — raw JSON only.

## Processing Logic (F)
%s

## Output Schema (B)
%s`, contract.ID, contract.Name, contract.ProcessingLogic, contract.OutputSchema)

	var userParts []string
	userParts = append(userParts, fmt.Sprintf("## Input Data (A)\n%s", inputData))

	if dbContext != "" {
		userParts = append(userParts, fmt.Sprintf("## Reference Database\n%s", dbContext))
	}

	userParts = append(userParts, "## Execute\nApply the processing logic (F) to the input (A) using the reference database. Output the result as JSON matching schema (B). JSON only — no other text.")

	userPrompt = strings.Join(userParts, "\n\n")
	return
}

func executeSheepContract(contract ContractDef, inputData, dbContext, vultrAPIKey, model string) (string, error) {
	systemPrompt, userPrompt := assembleContractPrompt(contract, inputData, dbContext)
	raw, err := vultrSheepCall(vultrAPIKey, model, systemPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("sheep contract execution failed (%s on %s): %w", contract.ID, model, err)
	}
	return raw, nil
}

func assemblePostconditionVerifyPrompt(contract ContractDef, sheepOutput string) string {
	return fmt.Sprintf(`You are a TPMN postcondition verifier (Wolfi). A sheep has executed contract %s and produced output.
Verify the output against ALL postconditions. Be strict — every check must pass.

## Contract: %s

## Postconditions (P)
%s

## Sheep Output (to verify)
%s

## Instructions
For each postcondition, check if the sheep output satisfies it.
Output ONLY valid JSON:
{
  "contract_id": "%s",
  "total_checks": <number>,
  "passed": <number>,
  "failed": <number>,
  "results": [
    {"id": "P1", "description": "...", "passed": true/false, "detail": "..."},
    ...
  ],
  "overall": "PASS" or "FAIL"
}`, contract.ID, contract.Name, contract.Postconditions, sheepOutput, contract.ID)
}
