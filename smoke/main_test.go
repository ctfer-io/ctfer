package smoke

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pkg/errors"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"gopkg.in/yaml.v3"
)

var (
	Coverdir string
)

func TestMain(m *testing.M) {
	if err := testmain(m); err != nil {
		fmt.Printf("FAILED: %s", err)
		os.Exit(1)
	}
}

func testmain(m *testing.M) error {
	// Compile the stack
	pwd, err := os.Getwd()
	if err != nil {
		return errors.Wrap(err, "get working directory")
	}
	pdir := filepath.Join(pwd, "..")
	cmd := exec.Command("go", "build", "-cover", "-o", "main", "main.go")
	cmd.Dir = pdir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "stack compilation failed with output: %s\n", out)
	}
	defer func() {
		_ = os.Remove(filepath.Join(pdir, "main"))
	}()

	// Re-write the Pulumi.yaml file to use the compiled binary
	b, err := os.ReadFile(filepath.Join(pdir, "Pulumi.yaml"))
	if err != nil {
		return errors.Wrap(err, "could not read Pulumi.yaml file")
	}
	var proj workspace.Project
	if err := yaml.Unmarshal(b, &proj); err != nil {
		return errors.Wrap(err, "invalid Pulumi.yaml content")
	}
	proj.Runtime.SetOption("binary", "./main")
	altered, err := yaml.Marshal(proj)
	if err != nil {
		return errors.Wrap(err, "marshalling Pulumi.yaml content")
	}
	if err := os.WriteFile(filepath.Join(pdir, "Pulumi.yaml"), altered, 0600); err != nil {
		return errors.Wrap(err, "writing back Pulumi.yaml")
	}
	defer func() {
		_ = os.WriteFile(filepath.Join(pdir, "Pulumi.yaml"), b, 0600) //nolint:gosec //#gosec G703 -- Don't bother with tests
	}()

	if code := m.Run(); code != 0 {
		return fmt.Errorf("exit with code %d", code)
	}
	return nil
}
