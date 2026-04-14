# Claude Pro/Max OAuth Support Implementation Plan

**Goal:** Add Claude Pro/Max as an LLM provider option — for both user tasks and the Bridge system LLM.

**Architecture:** This is just another LLM credential type. The OAuth setup-token is stored and handled like an API key. Gate injects it into requests via `x-api-key` header and adds the required OAuth beta headers. Skiff never sees real credentials (unchanged security model).

**Changes:**

1. **Gate** — When token type is `oauth_token`, inject via `x-api-key` + add `anthropic-beta: oauth-2025-04-20,claude-code-20250219`
2. **Credential store** — Add `oauth_token` to the auth type switch (return raw token like `api_key`)
3. **System LLM** — Add `claude-oauth` provider (same as `completeAnthropic` but with beta headers)
4. **Config** — Add `oauth_token` field to `SystemLLMConfig`
5. **One LLM per user** — API returns 409 if user already has an LLM credential; UI confirms replacement
6. **Dashboard UI** — Add "Claude Pro/Max" option with `claude setup-token` instructions
7. **Docs** — Update implementation-status.md
