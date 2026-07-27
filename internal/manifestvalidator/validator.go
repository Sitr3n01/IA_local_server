package manifestvalidator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/google/jsonschema-go/jsonschema"
)

func ValidateFiles(schemaPath, manifestPath string) error {
	schemaData, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	return Validate(schemaData, manifestData)
}

// Validate checks a JSON-compatible YAML manifest against its versioned JSON
// Schema. The production manifest deliberately uses JSON syntax, so no lossy
// YAML-to-JSON conversion is required here.
func Validate(schemaData, manifestData []byte) error {
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaData, &schema); err != nil {
		return fmt.Errorf("decode schema: %w", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fmt.Errorf("resolve schema: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(manifestData))
	var instance any
	if err := decoder.Decode(&instance); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode manifest: multiple JSON values")
		}
		return fmt.Errorf("decode manifest: %w", err)
	}
	if err := resolved.Validate(instance); err != nil {
		return fmt.Errorf("validate manifest: %w", err)
	}
	return nil
}
