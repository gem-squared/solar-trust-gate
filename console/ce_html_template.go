package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed ce_templates/ce_wrapper.html
var ceWrapperTemplateRaw string

var ceWrapperTemplate = template.Must(template.New("ce_wrapper").Parse(ceWrapperTemplateRaw))

// ceTemplateData feeds the HTML template (see ce_templates/ce_wrapper.html).
type ceTemplateData struct {
	ContractTitle string
	ProjectSlug   string
	WorkflowSlug  string
	StageSlug     string
	VultrModel    string
	APIEndpoint   string
	ASchema       string
	BSchema       string
	SampleI       string
}

// renderCEHTML produces a self-contained HTML test page for one CE.
// The returned bytes are written to {ProjectDir}/ce/{stage_slug}/index.html
// by executeCreateCE (WP-AO-30 Unit 2).
func renderCEHTML(spec *CESpec, productionHost, projectSlug string) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("nil spec")
	}
	sample := spec.SampleI
	if sample == "" {
		sample = sampleInputFromA(spec.A)
	}
	data := ceTemplateData{
		ContractTitle: spec.ContractTitle,
		ProjectSlug:   projectSlug,
		WorkflowSlug:  spec.WorkflowSlug,
		StageSlug:     spec.StageSlug,
		VultrModel:    spec.resolvedVultrModel(),
		APIEndpoint: fmt.Sprintf("%s/ce/%s/%s/",
			strings.TrimRight(productionHost, "/"),
			spec.WorkflowSlug, spec.StageSlug),
		ASchema: asString(spec.A),
		BSchema: asString(spec.B),
		SampleI: sample,
	}

	var buf bytes.Buffer
	if err := ceWrapperTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render ce html: %w", err)
	}
	return buf.String(), nil
}

// sampleInputFromA produces a JSON skeleton placeholder for the input form,
// inferring shape from the contract's A schema (which is typically a string
// description per the WP-AO-24 authoring guide). When A is unparseable, we
// emit an empty object so the textarea isn't blank.
func sampleInputFromA(a interface{}) string {
	if a == nil {
		return "{}"
	}
	// If A is already a JSON object (caller passed a structured shape),
	// render it as JSON-with-zero-values. For now keep it simple and emit
	// "{}" — the user fills in the form based on the visible A schema panel.
	return "{}"
}
