package workflow

import (
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

type Template struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	YAML             string    `json:"yaml"`
	Source           string    `json:"source,omitempty"`
	Kind             string    `json:"kind,omitempty"`
	Path             string    `json:"path,omitempty"`
	CreatedAt        time.Time `json:"created_at,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
	Valid            bool      `json:"valid"`
	ValidationStatus string    `json:"validation_status,omitempty"`
	Issues           []Issue   `json:"issues,omitempty"`
}

func LoadTemplates(source fs.FS, pattern string) ([]Template, error) {
	paths, err := fs.Glob(source, pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	var templates []Template
	for _, templatePath := range paths {
		content, err := fs.ReadFile(source, templatePath)
		if err != nil {
			return nil, err
		}
		preview := BuildPreview(string(content))
		id := strings.TrimSuffix(path.Base(templatePath), path.Ext(templatePath))
		templates = append(templates, Template{
			ID:               id,
			Name:             preview.Workflow.Name,
			Description:      preview.Workflow.Description,
			YAML:             string(content),
			Source:           DefinitionSourceEmbedded,
			Kind:             DefinitionKindWorkflow,
			Valid:            preview.CanRun,
			ValidationStatus: validationStatus(preview.CanRun),
			Issues:           preview.Issues,
		})
	}
	return templates, nil
}
