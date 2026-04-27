#!/bin/bash
set -e

# validate-triggers.sh - Ensure no duplicate trigger definitions exist
# Issue #480 fix: prevent agents from having trigger definitions when workflows handle triggers

echo "Validating trigger definitions..."

# Check that no agent files contain trigger definitions
echo "Checking for trigger definitions in agent files..."
if grep -r "^trigger:" .alcove/agents/ >/dev/null 2>&1; then
    echo "ERROR: Found trigger definitions in agent files:"
    grep -rn "^trigger:" .alcove/agents/
    echo "Agents should not have trigger definitions. Use workflows instead."
    exit 1
fi

# Check that workflow files do have proper trigger definitions
echo "Checking workflow trigger definitions..."
workflow_triggers=$(find .alcove/workflows/ -name "*.yml" -exec grep -l "^trigger:" {} \;)
if [ -z "$workflow_triggers" ]; then
    echo "WARNING: No workflow files have trigger definitions"
else
    echo "Found triggers in workflows:"
    for file in $workflow_triggers; do
        echo "  - $file"
    done
fi

# Check for consistency in agent descriptions
echo "Checking agent description consistency..."
if grep -r "Triggered by\|triggered by" .alcove/agents/ >/dev/null 2>&1; then
    echo "WARNING: Found agent descriptions mentioning direct triggers:"
    grep -rn "Triggered by\|triggered by" .alcove/agents/
    echo "Consider updating descriptions to reference workflow invocation instead."
fi

echo "Validation complete!"
