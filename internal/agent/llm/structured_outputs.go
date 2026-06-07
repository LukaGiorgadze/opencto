package llm

import openai "github.com/tmc/langchaingo/llms/openai"

func conversationCompressionResponseFormat() *openai.ResponseFormat {
	return jsonSchemaResponseFormat(
		"conversation_compression",
		objectSchema(
			map[string]*openai.ResponseFormatJSONSchemaProperty{
				"summary": stringSchema(),
			},
			"summary",
		),
	)
}

func agentObservationCompressionResponseFormat() *openai.ResponseFormat {
	return jsonSchemaResponseFormat(
		"agent_observation_compression",
		objectSchema(
			map[string]*openai.ResponseFormatJSONSchemaProperty{
				"summary": stringSchema(),
			},
			"summary",
		),
	)
}

func jsonSchemaResponseFormat(name string, schema *openai.ResponseFormatJSONSchemaProperty) *openai.ResponseFormat {
	return &openai.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &openai.ResponseFormatJSONSchema{
			Name:   name,
			Strict: true,
			Schema: schema,
		},
	}
}

func objectSchema(properties map[string]*openai.ResponseFormatJSONSchemaProperty, required ...string) *openai.ResponseFormatJSONSchemaProperty {
	return &openai.ResponseFormatJSONSchemaProperty{
		Type:                 "object",
		Properties:           properties,
		AdditionalProperties: false,
		Required:             required,
	}
}

func stringSchema() *openai.ResponseFormatJSONSchemaProperty {
	return &openai.ResponseFormatJSONSchemaProperty{Type: "string"}
}
