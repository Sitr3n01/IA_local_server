package manifestvalidator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryManifestMatchesSchema(t *testing.T) {
	schema, manifest := repositoryFiles(t)
	if err := Validate(schema, manifest); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaRejectsMissingLicenseWrongTypeAndUnknownProperty(t *testing.T) {
	schema, manifest := repositoryFiles(t)
	var valid map[string]any
	if err := json.Unmarshal(manifest, &valid); err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(map[string]any){
		"missing license": func(candidate map[string]any) {
			model := candidate["models"].([]any)[0].(map[string]any)
			delete(model["source"].(map[string]any), "license")
		},
		"wrong context type": func(candidate map[string]any) {
			candidate["models"].([]any)[0].(map[string]any)["context_tokens"] = "131072"
		},
		"unknown top-level property": func(candidate map[string]any) {
			candidate["unexpected"] = true
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			copyData, err := json.Marshal(valid)
			if err != nil {
				t.Fatal(err)
			}
			var candidate map[string]any
			if err := json.Unmarshal(copyData, &candidate); err != nil {
				t.Fatal(err)
			}
			mutate(candidate)
			encoded, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(schema, encoded); err == nil {
				t.Fatal("invalid manifest passed schema validation")
			}
		})
	}
}

func repositoryFiles(t *testing.T) ([]byte, []byte) {
	t.Helper()
	root := filepath.Join("..", "..", "config")
	schema, err := os.ReadFile(filepath.Join(root, "models.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return schema, manifest
}
