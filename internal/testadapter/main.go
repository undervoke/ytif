// Command testadapter stands in for a project adapter in tests. It replays
// a canned reply file on stdout and exits with a configured status, so
// tests exercise the real adapter subprocess protocol without project code.
//
//	YTIF_FAKE_RESPONSE   path to the reply bytes written to stdout
//	YTIF_FAKE_DISCOVER_RESPONSE, YTIF_FAKE_SELECT_RESPONSE, and
//	YTIF_FAKE_RUN_RESPONSE override it per operation, so one fixture can
//	answer discovery, selection, and runs differently
//	YTIF_FAKE_EXIT       exit status, default 0, overridden per operation by
//	YTIF_FAKE_<OPERATION>_EXIT
//	YTIF_FAKE_REQUEST_LOG  when set, the stdin request is saved to this path
//	YTIF_FAKE_STDERR     when set, written to stderr before the reply
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// operation reads the request's operation without validating the rest.
func operation(body []byte) string {
	var req struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	return req.Operation
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "testadapter:", err)
		os.Exit(2)
	}
}

func run() error {
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	if log := os.Getenv("YTIF_FAKE_REQUEST_LOG"); log != "" {
		if err := os.WriteFile(log, body, 0o644); err != nil {
			return err
		}
	}
	if noise := os.Getenv("YTIF_FAKE_STDERR"); noise != "" {
		if _, err := os.Stderr.WriteString(noise); err != nil {
			return err
		}
	}
	op := strings.ToUpper(operation(body))
	reply := os.Getenv("YTIF_FAKE_RESPONSE")
	if per := os.Getenv("YTIF_FAKE_" + op + "_RESPONSE"); per != "" {
		reply = per
	}
	data, err := os.ReadFile(reply)
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(data); err != nil {
		return err
	}
	code := os.Getenv("YTIF_FAKE_EXIT")
	if per := os.Getenv("YTIF_FAKE_" + op + "_EXIT"); per != "" {
		code = per
	}
	if code != "" {
		n, err := strconv.Atoi(code)
		if err != nil {
			return err
		}
		os.Exit(n)
	}
	return nil
}
