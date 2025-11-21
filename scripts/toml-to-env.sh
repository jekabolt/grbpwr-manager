#!/bin/sh
# Convert TOML config file to .env format

TOML_FILE="${1:-config/.config-local.toml}"
ENV_FILE="${2:-.env}"

if [ ! -f "$TOML_FILE" ]; then
    echo "Error: TOML file not found: $TOML_FILE"
    exit 1
fi

# Parse TOML and convert to env vars
awk '
BEGIN {
    section = ""
}
/^\[.*\]$/ {
    section = $0
    gsub(/\[|\]/, "", section)
    next
}
/^[[:space:]]*[^#]/ && /=/ {
    gsub(/^[[:space:]]+|[[:space:]]+$/, "")
    if (length($0) == 0) next
    
    # Split on first = only
    eq_pos = index($0, "=")
    if (eq_pos == 0) next
    
    key = substr($0, 1, eq_pos - 1)
    value = substr($0, eq_pos + 1)
    
    gsub(/^[[:space:]]+|[[:space:]]+$/, "", key)
    gsub(/^[[:space:]]+/, "", value)
    
    # Remove surrounding quotes if present
    quoted = 0
    if (match(value, /^".*"$/)) {
        value = substr(value, 2, length(value) - 2)
        quoted = 1
    }
    
    # Build env var name
    env_key = ""
    if (section != "") {
        section_upper = toupper(section)
        gsub(/-/, "_", section_upper)
        key_upper = toupper(key)
        gsub(/-/, "_", key_upper)
        env_key = section_upper "_" key_upper
    } else {
        env_key = toupper(key)
        gsub(/-/, "_", env_key)
    }
    
    # Handle array values
    if (value ~ /^\[.*\]$/) {
        print env_key "=\"" value "\""
    } else {
        # Escape special characters
        gsub(/\\/, "\\\\", value)
        gsub(/\$/, "\\$", value)
        gsub(/"/, "\\\"", value)
        print env_key "=\"" value "\""
    }
}
' "$TOML_FILE" > "$ENV_FILE"

echo "Generated .env file from $TOML_FILE"
echo "Output: $ENV_FILE"
