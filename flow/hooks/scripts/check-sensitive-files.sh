#!/bin/sh
# PreToolUse hook: warn before writing to sensitive files
# Receives the file path as the first argument from the hook context
# The hook system passes tool input as JSON on stdin

# Read the tool input from stdin
INPUT=$(cat)

# Extract the file path from the JSON input (handles both file_path and filePath)
FILE_PATH=$(echo "$INPUT" | grep -oE '"file_path"\s*:\s*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
if [ -z "$FILE_PATH" ]; then
  FILE_PATH=$(echo "$INPUT" | grep -oE '"filePath"\s*:\s*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
fi

if [ -z "$FILE_PATH" ]; then
  exit 0
fi

# Check against sensitive file patterns
case "$FILE_PATH" in
  *.env|*.env.*|*/.env|*/.env.*)
    echo "BLOCKED: Refusing to write to environment file: $FILE_PATH" >&2
    echo "Environment files may contain secrets. Edit manually if needed." >&2
    exit 2
    ;;
  *credentials*|*secrets*|*secret.*|*.pem|*.key|*.pfx|*.p12)
    echo "BLOCKED: Refusing to write to sensitive file: $FILE_PATH" >&2
    echo "This file may contain credentials or keys. Edit manually if needed." >&2
    exit 2
    ;;
  *id_rsa*|*id_ed25519*|*id_ecdsa*|*.keystore|*.jks)
    echo "BLOCKED: Refusing to write to key file: $FILE_PATH" >&2
    echo "This file contains cryptographic keys. Edit manually if needed." >&2
    exit 2
    ;;
esac

exit 0
