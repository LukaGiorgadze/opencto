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

func memoryExtractionResponseFormat() *openai.ResponseFormat {
	return jsonSchemaResponseFormat(
		"memory_extraction",
		objectSchema(
			map[string]*openai.ResponseFormatJSONSchemaProperty{
				"candidates": {
					Type: "array",
					Items: objectSchema(
						map[string]*openai.ResponseFormatJSONSchemaProperty{
							"scope": {
								Type: "string",
								Enum: []any{"user", "global", "project", "thread"},
							},
							"kind": {
								Type: "string",
								Enum: []any{
									"fact",
									"preference",
									"instruction",
									"decision",
									"constraint",
									"identity",
									"workflow",
									"reference",
									"feedback",
								},
							},
							"content": stringSchema(),
							"tags": {
								Type:  "array",
								Items: stringSchema(),
							},
							"confidence": {
								Type: "number",
							},
							"pinned": {
								Type: "boolean",
							},
							"reason": stringSchema(),
						},
						"scope",
						"kind",
						"content",
						"tags",
						"confidence",
						"pinned",
						"reason",
					),
				},
			},
			"candidates",
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
