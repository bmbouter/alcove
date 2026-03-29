-- 006_mcp_tools.sql
-- MCP tool registry for per-task tool selection.

CREATE TABLE IF NOT EXISTS mcp_tools (
    id           UUID PRIMARY KEY,
    name         TEXT NOT NULL,
    display_name TEXT NOT NULL,
    tool_type    TEXT NOT NULL DEFAULT 'custom',  -- 'builtin' or 'custom'
    mcp_command  TEXT,
    mcp_args     TEXT DEFAULT '[]',
    api_host     TEXT,                             -- e.g., "api.github.com"
    auth_header  TEXT DEFAULT 'Authorization',     -- header to inject credential
    auth_format  TEXT DEFAULT 'bearer',            -- 'bearer', 'header', 'basic'
    operations   JSONB NOT NULL DEFAULT '[]',      -- [{name, description, risk}]
    owner        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_tools_name_owner ON mcp_tools(name, owner);
