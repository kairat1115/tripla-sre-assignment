package terraform

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
)

func loadTemplates(dir string) (*template.Template, int, error) {
	templates := template.New("").Funcs(sprig.TxtFuncMap())
	count := 0

	err := filepath.WalkDir(dir, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(filePath, ".tmpl") {
			return nil
		}

		content, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read %s: %w", filePath, err)
		}
		name, err := filepath.Rel(dir, filePath)
		if err != nil {
			return fmt.Errorf("resolve template name for %s: %w", filePath, err)
		}
		name = filepath.ToSlash(name)
		if _, err := templates.New(name).Parse(string(content)); err != nil {
			return fmt.Errorf("parse %s: %w", name, err)
		}
		count++
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("scan %s: %w", dir, err)
	}
	if count == 0 {
		return nil, 0, fmt.Errorf("no .tmpl files found in %s", dir)
	}
	return templates, count, nil
}

func templateSignature(dir string) (string, error) {
	files := make([]string, 0)
	if err := filepath.WalkDir(dir, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(filePath, ".tmpl") {
			files = append(files, filePath)
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("scan %s: %w", dir, err)
	}
	sort.Strings(files)

	hash := sha256.New()
	for _, filePath := range files {
		name, err := filepath.Rel(dir, filePath)
		if err != nil {
			return "", fmt.Errorf("resolve template name for %s: %w", filePath, err)
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", filePath, err)
		}
		_, _ = hash.Write([]byte(filepath.ToSlash(name)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
