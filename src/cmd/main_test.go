package main

import (
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestAnalyzerHelp(t *testing.T) {
	if os.Getenv("TIMESYNC_TEST_ANALYZER_HELP") == "1" {
		flag.CommandLine = flag.NewFlagSet("analyzer", flag.ExitOnError)
		os.Args = []string{"analyzer", "-help"}
		main()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestAnalyzerHelp$")
	cmd.Env = append(os.Environ(), "TIMESYNC_TEST_ANALYZER_HELP=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("analyzer -help failed: %v\n%s", err, output)
	}
	help := string(output)
	for _, option := range []string{"-config", "-env"} {
		if !strings.Contains(help, option) {
			t.Errorf("analyzer help is missing %s:\n%s", option, help)
		}
	}
	if strings.Contains(help, "-listen") || strings.Contains(help, "-dashboard") {
		t.Errorf("analyzer exposes report service flags:\n%s", help)
	}
}
