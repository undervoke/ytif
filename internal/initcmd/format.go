package initcmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// formatFiles keeps formatting in the preflight: stdin carries the planned
// content, while the real target path selects the project's configuration.
func formatFiles(root string, files []file, output io.Writer) error {
	bin := filepath.Join(root, "node_modules", ".bin", "prettier")
	if info, err := os.Stat(bin); err != nil || info.IsDir() {
		return nil
	}
	if info, err := os.Stat(filepath.Join(root, "node_modules", ".bin", "biome")); err == nil && !info.IsDir() {
		fmt.Fprintln(output, "note: Prettier and Biome are both installed; automatic formatting was skipped")
		return nil
	}
	for i, f := range files {
		// Plugins are regenerated from templates even on a rerun. Compare the
		// formatted template so an existing project-formatted copy stays put.
		plugin := f.path == opencodeRel || f.path == kiloRel || f.path == piRel
		if f.want == nil || f.status == "unchanged" && !plugin {
			continue
		}
		cmd := exec.Command(bin, "--stdin-filepath", f.path)
		cmd.Dir = root
		cmd.Stdin = bytes.NewReader(f.want)
		var diagnostics bytes.Buffer
		cmd.Stderr = &diagnostics
		formatted, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("prettier formatting %s: %w\n%s", f.path, err, strings.TrimSpace(diagnostics.String()))
		}
		if diagnostics.Len() > 0 {
			fmt.Fprint(output, diagnostics.String())
		}
		files[i], err = compare(root, f.path, formatted)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "format: prettier %s\n", f.path)
	}
	return nil
}
