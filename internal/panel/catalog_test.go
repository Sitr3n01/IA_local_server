package panel

import (
	"path/filepath"
	"testing"
)

func TestCatalogExposesAllModelsAndDeploymentAvailability(t *testing.T) {
	path := writeTestFile(t, "models.yaml", testManifest("local-coding",
		testModel("local-coding", "candidate", "[\"canary\"]", true, true, true, true),
		testModel("final-only", "enabled", "[\"final\"]", true, true, true, true),
		testModel("retired-model", "retired", "[\"canary\"]", true, true, true, true),
	))
	catalog, err := LoadCatalog(path, EnvironmentCanary)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.PublicModel != "local-coding" || catalog.Environment != EnvironmentCanary {
		t.Fatalf("unexpected catalog identity: %+v", catalog)
	}
	all := catalog.AllModels()
	if len(all) != 3 || all[0].ID != "local-coding" || all[1].ID != "final-only" || all[2].ID != "retired-model" {
		t.Fatalf("manifest order was not preserved: %+v", all)
	}
	if !all[0].Available || !all[0].CanLaunchCodex() || !all[0].CanLaunchOpenCode() {
		t.Fatalf("public canary model should be launchable: %+v", all[0])
	}
	if all[1].Available || all[1].UnavailableReason == "" {
		t.Fatalf("final-only model should be unavailable: %+v", all[1])
	}
	if all[2].Available || all[2].UnavailableReason != "model state is retired" {
		t.Fatalf("retired model should be unavailable: %+v", all[2])
	}
	available := catalog.AvailableModels()
	if len(available) != 1 || available[0].ID != "local-coding" {
		t.Fatalf("available models = %+v", available)
	}

	all[0].Deployments[0] = "mutated"
	again, _ := catalog.Model("local-coding")
	if again.Deployments[0] != "canary" {
		t.Fatal("catalog leaked a mutable deployment slice")
	}
}

func TestCatalogRequiresJSONAndCompleteProjection(t *testing.T) {
	incomplete := "{\n" +
		"  \"schema_version\":1,\n" +
		"  \"provider\":{\"public_model\":\"local-coding\"},\n" +
		"  \"models\":[{\"id\":\"local-coding\",\"display_name\":\"Coding\",\"state\":\"candidate\",\"deployments\":[\"canary\"],\"context_tokens\":4096,\"max_output_tokens\":1024,\"capabilities\":{\"responses\":true}}]\n" +
		"}"
	tests := map[string]string{
		"yaml":           "schema_version: 1\nprovider:\n  public_model: local-coding\nmodels: []\n",
		"missing public": testManifest("missing", testModel("local-coding", "candidate", "[\"canary\"]", true, true, true, true)),
		"duplicate": testManifest("local-coding",
			testModel("local-coding", "candidate", "[\"canary\"]", true, true, true, true),
			testModel("local-coding", "candidate", "[\"canary\"]", true, true, true, true)),
		"incomplete capabilities": incomplete,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeTestFile(t, "models.yaml", contents)
			if _, err := LoadCatalog(path, EnvironmentCanary); err == nil {
				t.Fatal("invalid manifest was accepted")
			}
		})
	}
}

func TestCatalogAllowsClientsForAnyAvailableModel(t *testing.T) {
	path := writeTestFile(t, "models.yaml", testManifest("chat-only",
		testModel("chat-only", "enabled", "[\"canary\"]", false, true, true, false),
	))
	catalog, err := LoadCatalog(path, EnvironmentCanary)
	if err != nil {
		t.Fatal(err)
	}
	model, _ := catalog.Model("chat-only")
	if !model.CanLaunchCodex() || !model.CanLaunchOpenCode() {
		t.Fatalf("available model should remain selectable: %+v", model)
	}
}

func TestRepositoryManifestLoadsAsCanaryCatalog(t *testing.T) {
	path := filepath.Join("..", "..", "config", "models.yaml")
	catalog, err := LoadCatalog(path, EnvironmentCanary)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.PublicModel != "local-coding" {
		t.Fatalf("public model = %q", catalog.PublicModel)
	}
	if len(catalog.AllModels()) < 1 || len(catalog.AvailableModels()) < 1 {
		t.Fatal("repository manifest did not expose an available canary model")
	}
}
