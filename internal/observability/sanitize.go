package observability

// SanitizeBusinessValue is deliberately an identity transform. S08 exports
// business payloads (including document capability tokens and URLs) verbatim
// to the operator's isolated self-hosted Langfuse Project. Runtime secrets are
// excluded at their typed sources: config is never an observation payload and
// the OTLP transport owns its Authorization header outside this value path.
func SanitizeBusinessValue(value any) any { return value }
