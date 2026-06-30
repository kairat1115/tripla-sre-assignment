package service

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"

	"github.com/kairat1115/tripla-sre-assignment/terraform_parse_service/internal/storage"
)

type Generator interface {
	Provider() string
	TemplateName() string
	StoragePath() string
	TemplateData() any
}

type TerraformService struct {
	writers   map[string]storage.Writer
	templates map[string]*template.Template
}

func NewTerraformService(writers map[string]storage.Writer, templates map[string]*template.Template) *TerraformService {
	return &TerraformService{writers: writers, templates: templates}
}

func LoadTemplates(dir string) (*template.Template, error) {
	tmpl := template.New("").Funcs(sprig.TxtFuncMap())
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return err
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read template %s: %w", path, readErr)
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if _, parseErr := tmpl.New(rel).Parse(string(content)); parseErr != nil {
			return fmt.Errorf("parse template %s: %w", rel, parseErr)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load templates from %s: %w", dir, err)
	}
	return tmpl, nil
}

func (s *TerraformService) Generate(g Generator) (string, error) {
	tmpl, ok := s.templates[g.Provider()]
	if !ok {
		return "", fmt.Errorf("no templates registered for provider %s", g.Provider())
	}
	writer, ok := s.writers[g.Provider()]
	if !ok {
		return "", fmt.Errorf("no writer registered for provider %s", g.Provider())
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, g.TemplateName(), g.TemplateData()); err != nil {
		return "", fmt.Errorf("render template %s: %w", g.TemplateName(), err)
	}
	path, err := writer.Write(g.StoragePath(), buf.Bytes())
	if err != nil {
		return "", fmt.Errorf("write storage: %w", err)
	}
	return path, nil
}
