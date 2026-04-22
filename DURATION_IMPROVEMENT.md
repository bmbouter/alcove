# Session Duration Display Enhancement

This enhancement improves the session duration display in the `alcove list` command to make it more user-friendly.

## Changes Made

### 1. Enhanced Duration Formatting

Added `formatDurationForDisplay()` function that:
- Displays durations in a human-readable format (e.g., "2h30m" instead of "2h30m15.123s")
- Shows elapsed time for running sessions with a "*" indicator
- Shows "-" for sessions without duration data

### 2. Running Session Support

For sessions that are still running, the CLI now:
- Calculates the elapsed time from the start timestamp
- Displays it with a "*" suffix to indicate it's still running
- Example: "15m*" means the session has been running for approximately 15 minutes

### 3. Duration Format Examples

- Very short: "30s" (30 seconds)
- Short: "5m30s" (5 minutes, 30 seconds)
- Medium: "2h30m" (2 hours, 30 minutes - seconds are omitted for longer durations)
- Long: "24h" (24 hours exactly)
- Running: "1h15m*" (running for about 1 hour 15 minutes)
- No data: "-" (session has no duration information)

### 4. Example Output

Before enhancement:
```
ID                                   STATUS     REPO        PROVIDER   DURATION             PROMPT
12345678-abcd-1234-abcd-123456789012 completed  org/repo    anthropic  2h30m15.123456789s   Fix the login bug
87654321-dcba-4321-dcba-210987654321 running    org/repo    anthropic                       Update documentation
```

After enhancement:
```
ID                                   STATUS     REPO        PROVIDER   DURATION   PROMPT
12345678-abcd-1234-abcd-123456789012 completed  org/repo    anthropic  2h30m      Fix the login bug
87654321-dcba-4321-dcba-210987654321 running    org/repo    anthropic  45m*       Update documentation
```

## Testing

Added comprehensive unit tests for:
- Duration formatting in various time ranges
- Running session handling with multiple timestamp formats (RFC3339, RFC3339Nano, etc.)
- Edge cases like invalid duration formats and malformed timestamps

## Backward Compatibility

- All existing JSON output remains unchanged
- Table output format is preserved, only duration column content is enhanced
- No breaking changes to API or CLI interface
