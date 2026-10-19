//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
)

func main() {
	if err := buildResources(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func buildResources() error {
	source, err := os.ReadFile("version.go")
	if err != nil {
		return err
	}
	match := regexp.MustCompile(`const appVersion = "([^"]+)"`).FindSubmatch(source)
	if len(match) != 2 {
		return fmt.Errorf("could not read appVersion")
	}
	version := string(match[1])
	raw, err := os.ReadFile("winres/winres.json")
	if err != nil {
		return err
	}
	var resources map[string]map[string]map[string]any
	if err := json.Unmarshal(raw, &resources); err != nil {
		return err
	}
	metadata := resources["RT_VERSION"]["#1"]["0000"].(map[string]any)
	fixed := metadata["fixed"].(map[string]any)
	fixed["file_version"] = version + ".0"
	fixed["product_version"] = version + ".0"
	info := metadata["info"].(map[string]any)["0409"].(map[string]any)
	info["FileVersion"] = version
	info["ProductVersion"] = version
	info["OriginalFilename"] = "soundstreamer-" + version + ".exe"
	data, err := json.Marshal(resources)
	if err != nil {
		return err
	}
	config, err := os.CreateTemp("winres", ".soundstreamer-winres-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(config.Name())
	if _, err := config.Write(data); err != nil {
		config.Close()
		return err
	}
	if err := config.Close(); err != nil {
		return err
	}
	cmd := exec.Command("go", "run", "github.com/tc-hib/go-winres@v0.3.3", "make", "--in", config.Name(), "--out", "rsrc", "--arch", "amd64")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
