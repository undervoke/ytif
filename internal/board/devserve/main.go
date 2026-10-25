// Command devserve serves the board of a repository for developing the page.
// It reads internal/board/assets on every request, so an edit to the page
// shows on reload without a rebuild. Run it from the ytif repository root:
//
//	go run ./internal/board/devserve <repo>
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/board"
	"github.com/undervoke/ytif/internal/cirecords"
	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/record"
	"github.com/undervoke/ytif/internal/storage"
)

const addr = "127.0.0.1:8765"

func main() {
	if len(os.Args) != 2 {
		fail("usage: go run ./internal/board/devserve <repo>")
	}
	root, err := gitx.Root(os.Args[1])
	if err != nil {
		fail("devserve: %s is not inside a git repository: %v", os.Args[1], err)
	}
	common, err := gitx.CommonDir(root)
	if err != nil {
		fail("devserve: %v", err)
	}
	assets := os.DirFS(filepath.Join("internal", "board", "assets"))
	if _, err := fs.Stat(assets, "board.html"); err != nil {
		fail("devserve: run from the ytif repository root: %v", err)
	}

	http.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		html, err := render(assets, root, common)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(html)
	})
	fmt.Printf("board of %s at http://%s\n", root, addr)
	fail("devserve: %v", http.ListenAndServe(addr, nil))
}

func render(assets fs.FS, root, common string) ([]byte, error) {
	now := time.Now()
	status, maintenanceErr := record.Maintain(common, now)
	if errors.Is(maintenanceErr, storage.ErrBusy) {
		if receipt, err := storage.ReadReceipt(common); err == nil {
			status = receipt.Local
		}
	} else if maintenanceErr != nil {
		fmt.Fprintf(os.Stderr, "devserve: record maintenance: %v\n", maintenanceErr)
	}
	lines, _, err := record.ReadManaged(common)
	if err != nil {
		fmt.Fprintf(os.Stderr, "devserve: records: %v\n", err)
		status.Error = err.Error()
	}
	src := board.Sources{Local: lines, LocalStorage: &status}
	if _, ok := cirecords.Origin(root); ok {
		result, err := (cirecords.Client{Token: cirecords.Token()}).Sync(root, common, now)
		if err != nil {
			fmt.Fprintf(os.Stderr, "devserve: CI records: %s; showing cached records\n", strings.ReplaceAll(err.Error(), "\n", "; "))
		}
		src.Remote, src.RemoteBranch, src.Status = result.Lines, result.Branch, &result.Status
	}
	if src.Branch, err = gitx.Branch(root); err != nil {
		return nil, err
	}
	return board.Render(assets, root, src, now)
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(2)
}
