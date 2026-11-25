#!/usr/bin/env python3
"""PreToolUse hook for Bash: the tool's shell is zsh, which does not split
an unquoted $var into words (a loop over "$v" ran one setting silently).
Commands with loops, `set -- $...` or a list assigned from $(...) must run
under bash: `bash -c '...'` or a bash script. Heredoc bodies (Python and
the like) are not looked at."""
import json
import re
import sys

try:
    cmd = json.load(sys.stdin).get("tool_input", {}).get("command", "")
except Exception:
    sys.exit(0)

# Drop heredoc bodies: <<'EOF' ... EOF, <<EOF ... EOF, <<-EOF ... EOF.
shell = re.sub(r"<<-?\s*['\"]?(\w+)['\"]?[^\n]*\n.*?\n\s*\1\b", "<<heredoc", cmd, flags=re.S)

# Under bash already: `bash -c ...`, `bash script`, `sh script`.
if re.search(r"(^|[;&|(\s])(bash|sh)\s+(-c\s|[\w./~$\"'-])", shell):
    sys.exit(0)

patterns = [
    (r"(^|[;&|(\s])(for|while|until)\s+[^\n]*?(\sdo\b|;\s*do\b)", "a loop"),
    (r"\bset\s+--\s+\$", "set -- $..."),
    (r"(^|[;&|(\s])\w+=\$\(\s*(ls|find|seq|cat|grep|echo|printf|git)\b", "a list assigned from $(...)"),
]
for pat, what in patterns:
    if re.search(pat, shell):
        reason = (
            f"This command has {what} and the Bash tool runs zsh, which does not split "
            "an unquoted $var into words (CLAUDE.md: loops and multi-word variables only "
            "through bash). Put it in a bash script (scripts/...) and run "
            "`bash script.sh`, or wrap it as `bash -c '...'`."
        )
        print(json.dumps({"hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        }}))
        sys.exit(0)
sys.exit(0)
