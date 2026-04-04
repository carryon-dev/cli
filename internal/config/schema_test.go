package config

import "testing"

func TestValidateString(t *testing.T) {
	val, err := ValidateKey("default.backend", "native")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "native" {
		t.Fatalf("expected native, got %v", val)
	}
}

func TestValidateStringEnum(t *testing.T) {
	_, err := ValidateKey("default.backend", "invalid")
	if err == nil {
		t.Fatal("expected error for invalid enum value")
	}
}

func TestValidateNumber(t *testing.T) {
	val, err := ValidateKey("local.port", "9000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 9000 {
		t.Fatalf("expected 9000, got %v", val)
	}
}

func TestValidateNumberRange(t *testing.T) {
	_, err := ValidateKey("local.port", "100")
	if err == nil {
		t.Fatal("expected error for port below min")
	}
}

func TestValidateBool(t *testing.T) {
	val, err := ValidateKey("local.enabled", "true")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != true {
		t.Fatalf("expected true, got %v", val)
	}
}

func TestValidateBoolInvalid(t *testing.T) {
	_, err := ValidateKey("local.enabled", "yes")
	if err == nil {
		t.Fatal("expected error for non-bool value")
	}
}

func TestValidateUnknownKey(t *testing.T) {
	_, err := ValidateKey("nonexistent.key", "value")
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestGetAllDefaults(t *testing.T) {
	defaults := GetAllDefaults()
	if defaults["default.backend"] != "native" {
		t.Fatalf("expected native, got %v", defaults["default.backend"])
	}
	if defaults["local.port"] != 8384 {
		t.Fatalf("expected 8384, got %v", defaults["local.port"])
	}
}

func TestSchemaHasDisplayMetadata(t *testing.T) {
	for key, def := range Schema {
		if def.name == "" {
			t.Errorf("key %q is missing display name", key)
		}
		if def.desc == "" {
			t.Errorf("key %q is missing description", key)
		}
		if def.group == "" {
			t.Errorf("key %q is missing group", key)
		}
	}
}

func TestGroupsCoverAllKeys(t *testing.T) {
	groupKeys := make(map[string]bool)
	for _, g := range Groups {
		groupKeys[g.key] = true
	}
	for key, def := range Schema {
		if !groupKeys[def.group] {
			t.Errorf("key %q has group %q but no matching group definition", key, def.group)
		}
	}
}

func TestKeyOrderMatchesSchema(t *testing.T) {
	if len(KeyOrder) != len(Schema) {
		t.Fatalf("KeyOrder has %d entries but Schema has %d", len(KeyOrder), len(Schema))
	}
	for _, key := range KeyOrder {
		if _, ok := Schema[key]; !ok {
			t.Errorf("KeyOrder contains %q which is not in Schema", key)
		}
	}
}

func TestBuildSchemaExcludesHiddenKeys(t *testing.T) {
	mgr := NewManager(t.TempDir())
	result := BuildSchema(mgr)
	groups := result["groups"].([]map[string]any)

	for _, g := range groups {
		if g["settings"] == nil {
			continue
		}
		settings := g["settings"].([]map[string]any)
		for _, s := range settings {
			if s["key"] == "local.password" {
				t.Error("hidden key 'local.password' should not appear in BuildSchema output")
			}
		}
	}
}

func TestBuildSchemaStructure(t *testing.T) {
	mgr := NewManager(t.TempDir())
	result := BuildSchema(mgr)

	if result["schemaVersion"] != 1 {
		t.Fatalf("expected schemaVersion 1, got %v", result["schemaVersion"])
	}

	groups, ok := result["groups"].([]map[string]any)
	if !ok {
		t.Fatalf("expected groups to be []map[string]any, got %T", result["groups"])
	}
	if len(groups) != len(Groups) {
		t.Fatalf("expected %d groups, got %d", len(Groups), len(groups))
	}

	for i, g := range groups {
		if g["key"] != Groups[i].key {
			t.Errorf("group %d: expected key %q, got %q", i, Groups[i].key, g["key"])
		}
		if g["name"] != Groups[i].name {
			t.Errorf("group %d: expected name %q, got %q", i, Groups[i].name, g["name"])
		}
		if g["description"] != Groups[i].desc {
			t.Errorf("group %d: expected description %q, got %q", i, Groups[i].desc, g["description"])
		}
	}
}

func TestBuildSchemaSettingsInGroups(t *testing.T) {
	mgr := NewManager(t.TempDir())
	result := BuildSchema(mgr)
	groups := result["groups"].([]map[string]any)

	var defaultGroup map[string]any
	for _, g := range groups {
		if g["key"] == "default" {
			defaultGroup = g
			break
		}
	}
	if defaultGroup == nil {
		t.Fatal("missing 'default' group")
	}

	settings, ok := defaultGroup["settings"].([]map[string]any)
	if !ok {
		t.Fatalf("expected settings to be []map[string]any, got %T", defaultGroup["settings"])
	}
	if len(settings) != 2 {
		t.Fatalf("expected 2 settings in default group, got %d", len(settings))
	}

	s := settings[0]
	if s["key"] != "default.backend" {
		t.Errorf("expected first setting key 'default.backend', got %q", s["key"])
	}
	if s["name"] != "Default Backend" {
		t.Errorf("expected name 'Default Backend', got %q", s["name"])
	}
	if s["type"] != "string" {
		t.Errorf("expected type 'string', got %q", s["type"])
	}
	if s["default"] != "native" {
		t.Errorf("expected default 'native', got %v", s["default"])
	}
	if s["value"] != "native" {
		t.Errorf("expected value 'native', got %v", s["value"])
	}
	enum, ok := s["enum"].([]string)
	if !ok || len(enum) != 2 {
		t.Errorf("expected enum with 2 values, got %v", s["enum"])
	}
}

func TestBuildSchemaOmitsOptionalFields(t *testing.T) {
	mgr := NewManager(t.TempDir())
	result := BuildSchema(mgr)
	groups := result["groups"].([]map[string]any)

	for _, g := range groups {
		settings := g["settings"].([]map[string]any)
		for _, s := range settings {
			if s["key"] == "local.enabled" {
				if _, ok := s["enum"]; ok {
					t.Error("local.enabled should not have enum field")
				}
				if _, ok := s["min"]; ok {
					t.Error("local.enabled should not have min field")
				}
				if _, ok := s["max"]; ok {
					t.Error("local.enabled should not have max field")
				}
			}
			if s["key"] == "local.port" {
				if s["min"] != 1024 {
					t.Errorf("local.port min: expected 1024, got %v", s["min"])
				}
				if s["max"] != 65535 {
					t.Errorf("local.port max: expected 65535, got %v", s["max"])
				}
			}
		}
	}
}

func TestBuildSchemaReflectsCurrentValues(t *testing.T) {
	mgr := NewManager(t.TempDir())
	mgr.Set("local.port", "9000")

	result := BuildSchema(mgr)
	groups := result["groups"].([]map[string]any)

	for _, g := range groups {
		settings := g["settings"].([]map[string]any)
		for _, s := range settings {
			if s["key"] == "local.port" {
				if s["value"] != 9000 {
					t.Errorf("expected current value 9000, got %v", s["value"])
				}
				if s["default"] != 8384 {
					t.Errorf("expected default 8384, got %v", s["default"])
				}
				return
			}
		}
	}
	t.Fatal("local.port not found in schema")
}
