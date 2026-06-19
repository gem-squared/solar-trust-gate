package main

type CheckType string

const (
	CheckArithmetic  CheckType = "arithmetic"
	CheckEnum        CheckType = "enum"
	CheckRange       CheckType = "range"
	CheckConsistency CheckType = "consistency"
	CheckCompleteness CheckType = "completeness"
	CheckSPT         CheckType = "spt"
)

type ContractField struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Enum     []string `json:"enum,omitempty"`
	Nullable bool     `json:"nullable,omitempty"`
}

type Postcondition struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	CheckType   CheckType `json:"check_type"`
	Expression  string    `json:"expression,omitempty"`
}

type WorkflowStep struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	Domain              string            `json:"domain"`
	Index               int               `json:"index"`
	InputFields         []ContractField   `json:"input_fields"`
	OutputFields        []ContractField   `json:"output_fields"`
	Postconditions      []Postcondition   `json:"postconditions"`
	FunctionDescription string            `json:"function_description"`
	SampleInputA        map[string]any    `json:"sample_input_a"`
}

type Workflow struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Domain string        `json:"domain"`
	Steps []WorkflowStep `json:"steps"`
}
