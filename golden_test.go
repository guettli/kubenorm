package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the want files of testdata/golden and testdata/golden-diff")

// TestGolden normalizes rendered helm charts (scripts/render-golden-inputs.sh)
// and compares the result with want.yaml. Run with -update after a deliberate
// change and review the diff.
func TestGolden(t *testing.T) {
	t.Parallel()

	dirs, err := filepath.Glob("testdata/golden/*")
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()

			args, err := os.ReadFile(filepath.Join(dir, "args"))
			if err != nil {
				t.Fatal(err)
			}

			cliArgs := append([]string{"norm", filepath.Join(dir, "input.yaml"), "--no-config-discovery"}, strings.Fields(string(args))...)

			code, stdout, stderr := runCLI(t, "", cliArgs...)
			if code != 0 {
				t.Fatalf("exit code %d: %s", code, stderr)
			}

			got := stripHeader(stdout)
			wantFile := filepath.Join(dir, "want.yaml")

			if *update {
				err = os.WriteFile(wantFile, []byte(got), 0o600) //nolint:gosec // path comes from testdata/golden
				if err != nil {
					t.Fatal(err)
				}
			}

			want, err := os.ReadFile(wantFile)
			if err != nil {
				t.Fatal(err)
			}

			if got != string(want) {
				t.Errorf("output differs from %s, run: go test -run TestGolden -update", wantFile)
			}

			// Idempotent: normalizing the output again changes nothing.
			cliArgs[1] = wantFile

			code, stdout, stderr = runCLI(t, "", cliArgs...)
			if code != 0 {
				t.Fatalf("second pass: exit code %d: %s", code, stderr)
			}

			if again := stripHeader(stdout); again != got {
				t.Errorf("not idempotent: normalizing %s changes it", wantFile)
			}
		})
	}
}

// TestGoldenDiff runs kubenorm diff on testdata/golden-diff/input/{old,new}
// with the args of each case in testdata/golden-diff/cases and compares the
// output with want.txt, so a change of the output format shows up in review.
func TestGoldenDiff(t *testing.T) {
	t.Parallel()

	cases, err := filepath.Glob("testdata/golden-diff/cases/*")
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range cases {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()

			args, err := os.ReadFile(filepath.Join(dir, "args"))
			if err != nil {
				t.Fatal(err)
			}

			cliArgs := append([]string{"diff", "--no-config-discovery", "testdata/golden-diff/input/old", "testdata/golden-diff/input/new"}, strings.Fields(string(args))...)

			code, got, stderr := runCLI(t, "", cliArgs...)
			if code != 1 {
				t.Fatalf("exit code %d, want 1: %s", code, stderr)
			}

			wantFile := filepath.Join(dir, "want.txt")

			if *update {
				err = os.WriteFile(wantFile, []byte(got), 0o600) //nolint:gosec // path comes from testdata/golden-diff
				if err != nil {
					t.Fatal(err)
				}
			}

			want, err := os.ReadFile(wantFile)
			if err != nil {
				t.Fatal(err)
			}

			if got != string(want) {
				t.Errorf("output differs from %s, run: go test -run TestGoldenDiff -update", wantFile)
			}
		})
	}
}
