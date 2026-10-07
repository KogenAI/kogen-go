package wire

import "encoding/json"

// CanonicalToolSchemas returns the complete ordered seven-schema union. The
// caller's role allowlist belongs in tool_choice; it must never filter this
// static cacheable schema set.
func CanonicalToolSchemas() []json.RawMessage {
	return []json.RawMessage{
		functionSchema("edit", "Replace one exact text span in a workspace file.", []string{"path", "old", "new"}, map[string]any{
			"path": stringSchema(), "old": stringSchema(), "new": stringSchema(),
		}, false),
		functionSchema("finish", "Finish the implementation and request the gate.", []string{}, map[string]any{}, true),
		functionSchema("read", "Read a UTF-8 workspace file by line range.", []string{"path"}, map[string]any{
			"path":   stringSchema(),
			"offset": map[string]any{"type": "integer", "minimum": 1},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 400},
		}, false),
		functionSchema("search", "Search workspace files with ripgrep.", []string{"pattern"}, map[string]any{
			"pattern": stringSchema(), "path": stringSchema(),
		}, false),
		functionSchema("shell", "Run a shell command in the isolated workspace.", []string{"cmd"}, map[string]any{
			"cmd": stringSchema(),
		}, false),
		functionSchema("tool_output", "Retrieve a stored tool result by handle and byte range.", []string{"handle"}, map[string]any{
			"handle":        stringSchema(),
			"output_offset": map[string]any{"type": "integer", "minimum": 0},
			"output_limit":  map[string]any{"type": "integer", "minimum": 0},
		}, false),
		functionSchema("write", "Write exact UTF-8 text to a workspace file.", []string{"path", "content"}, map[string]any{
			"path": stringSchema(), "content": stringSchema(),
		}, false),
	}
}

func functionSchema(name, description string, required []string, properties map[string]any, strict bool) json.RawMessage {
	schema := map[string]any{
		"type":        "function",
		"name":        name,
		"description": description,
		"parameters": map[string]any{
			"type": "object", "properties": properties,
			"required": required, "additionalProperties": false,
		},
		"strict": strict,
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return encoded
}

func stringSchema() map[string]any { return map[string]any{"type": "string"} }
