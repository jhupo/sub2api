export function buildCodexApiKeyAuthConfig(apiKey: string): string {
  return `requires_openai_auth = false
experimental_bearer_token = ${JSON.stringify(apiKey)}
http_headers = { "x-openai-actor-authorization" = "local-image-extension" }`
}
