#!/bin/sh
set -eu

go mod tidy

changes=$(git status --porcelain -- go.mod go.sum)
if [ -z "$changes" ]; then
    echo "Go module files are tidy"
    exit 0
fi

echo "go mod tidy changed tracked module files:" >&2
echo "$changes" >&2

git diff -- go.mod go.sum || true

if [ -f go.sum ] && ! git ls-files --error-unmatch go.sum >/dev/null 2>&1; then
    echo "--- generated go.sum ---" >&2
    cat go.sum >&2
    echo "--- end generated go.sum ---" >&2
fi

exit 1
