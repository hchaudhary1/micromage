package workflow

type Preview struct {
	Workflow Workflow  `json:"workflow"`
	Graph    GraphView `json:"graph"`
	Issues   []Issue   `json:"issues"`
	CanRun   bool      `json:"can_run"`
}

func BuildPreview(input string) Preview {
	workflow, issues := ParseYAML(input)
	return buildPreview(workflow, issues)
}

func BuildPreviewWithCommands(input string, registry CommandRegistry) Preview {
	workflow, issues := ParseYAML(input)
	issues = append(issues, ValidateCommands(workflow, registry)...)
	return buildPreview(workflow, issues)
}

func buildPreview(workflow Workflow, issues []Issue) Preview {
	preview := Preview{
		Workflow: workflow,
		Issues:   issues,
		CanRun:   !HasErrors(issues),
	}
	if workflow.Name != "" || len(workflow.Nodes) > 0 {
		preview.Graph = BuildGraph(workflow, issues)
	}
	return preview
}
