package checkpoint

// defaultGitignore keeps data and model artefacts, caches and local
// environments out of Checkpoints.
const defaultGitignore = `# Written by Lamplight. Checkpoints save every file in this Topic that is not
# ignored here, so keep large data and models out. Edit freely.

# Data
*.parquet
*.duckdb
*.duckdb.wal
*.arrow
*.feather

# Models and checkpoints
*.gguf
*.safetensors
*.pt
*.pth
*.ckpt
*.onnx

# Build outputs
target/
build/
dist/
*.o
a.out

# Test coverage, which Checks often write
.coverage
.coverage.*
coverage.out

# Caches and environments
__pycache__/
*.py[cod]
.pytest_cache/
.mypy_cache/
.ruff_cache/
.ipynb_checkpoints/
.venv/
node_modules/
.cache/
.DS_Store

# Secrets
.env

# Temporary files Lamplight leaves if a write is interrupted
.*.lamplight-tmp-*

# Held-out data is committed with the Topic, whatever its format, so every
# machine measures on the same data. Keep this last.
!.heldout/**
`

// DefaultGitignore returns the .gitignore written into a new Topic: data and
// model artefacts (Parquet, DuckDB, GGUF, safetensors, PyTorch checkpoints),
// common caches and environments, and .env files.
func DefaultGitignore() string {
	return defaultGitignore
}
