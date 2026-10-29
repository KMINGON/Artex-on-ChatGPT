package agent

// Batch items expose the same contract as the single-item form. Previously the
// empty object schema hid optional provenance fields from deferred-tool lookup.
// Item requirements are opt-in so partial-success handlers can validate each item.
func withBatchSchema(schema map[string]any, batch string, required ...string) map[string]any {
	props := schema["properties"].(map[string]any)
	fields := make(map[string]any, len(props)-1)
	for name, field := range props {
		if name != batch {
			fields[name] = field
		}
	}
	props[batch].(map[string]any)["items"] = obj(fields, required...)
	return schema
}
