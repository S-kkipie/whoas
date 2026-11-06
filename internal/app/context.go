package app

import "os"

// contextEnv is the WHOAS_CONTEXT_DIR entry every process whoas starts or
// execs gets: the directory whoas resolved from (its working directory). An
// agent started through whoas passes it on to the MCP proxies it spawns, so
// they resolve identities for the same project. Nested shims overwrite it
// with their own working directory.
func contextEnv() []string {
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	return []string{"WHOAS_CONTEXT_DIR=" + wd}
}

// withContext returns env with WHOAS_CONTEXT_DIR set as by contextEnv.
func withContext(env []string) []string { return setEnv(env, contextEnv()...) }

// mcpContextDir is the directory `whoas mcp proxy` resolves .whoas.yaml from:
// the WHOAS_CONTEXT_DIR inherited from a whoas parent, else the working
// directory (an agent started without whoas).
func mcpContextDir() (string, error) {
	if d := os.Getenv("WHOAS_CONTEXT_DIR"); d != "" {
		return d, nil
	}
	return os.Getwd()
}
