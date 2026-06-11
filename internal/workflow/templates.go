package workflow

import (
	"io/fs"
	"path"
	"sort"
	"strings"
)

type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	YAML        string `json:"yaml"`
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
		// TODO: Replace embedded-only templates with project/global workflow discovery when persistence lands.
		templates = append(templates, Template{
			ID:          id,
			Name:        preview.Workflow.Name,
			Description: preview.Workflow.Description,
			YAML:        string(content),
		})
	}
	return templates, nil
}
