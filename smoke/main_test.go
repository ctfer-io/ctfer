package smoke

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"gopkg.in/yaml.v3"
)

var (
	Coverdir string
)

func TestMain(m *testing.M) {
	// Compile the stack
	pwd, err := os.Getwd()
	if err != nil {
		fmt.Printf("Get working directory: %s\n", err)
		os.Exit(1)
	}
	pdir := filepath.Join(pwd, "..")
	cmd := exec.Command("go", "build", "-cover", "-o", "main", "main.go")
	cmd.Dir = pdir
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Stack compilation failed: %s ; output: %s\n", err, out)
		os.Exit(1)
	}
	defer func() {
		_ = os.Remove(filepath.Join(pdir, "main"))
	}()

	// Re-write the Pulumi.yaml file to use the compiled binary
	b, err := os.ReadFile(filepath.Join(pdir, "Pulumi.yaml"))
	if err != nil {
		fmt.Printf("Could not read Pulumi.yaml file: %s\n", err)
		os.Exit(1)
	}
	var proj workspace.Project
	if err := yaml.Unmarshal(b, &proj); err != nil {
		fmt.Printf("Invalid Pulumi.yaml content: %s\n", err)
		os.Exit(1)
	}
	proj.Runtime.SetOption("binary", "./main")
	altered, err := yaml.Marshal(proj)
	if err != nil {
		fmt.Printf("Marshalling Pulumi.yaml content: %s\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(pdir, "Pulumi.yaml"), altered, 0644); err != nil {
		fmt.Printf("Writing back Pulumi.yaml: %s\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = os.WriteFile(filepath.Join(pdir, "Pulumi.yaml"), b, 0644)
	}()

	os.Exit(m.Run())
}
