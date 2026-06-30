package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func runStatus(args []string) {
	project, dataDir, err := parseStatusArgs(args)
	if err != nil {
		if errors.Is(err, errStatusUsage) {
			printStatusUsage()
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		printStatusUsage()
		os.Exit(1)
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}

	url := fmt.Sprintf("http://unix/project/status?project=%s", project)
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to connect to daemon: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", strings.TrimSpace(string(body)))
		os.Exit(1)
	}

	fmt.Println(strings.TrimSpace(string(body)))
}

var errStatusUsage = errors.New("show status usage")

func parseStatusArgs(args []string) (string, string, error) {
	var project string
	var dataDir string
	var configPath string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--project requires a value")
			}
			i++
			project = args[i]
		case "--config", "-c":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--config requires a value")
			}
			i++
			configPath = args[i]
		case "--data-dir":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--data-dir requires a value")
			}
			i++
			dataDir = args[i]
		case "--help", "-h":
			return "", "", errStatusUsage
		default:
			if project == "" && !strings.HasPrefix(args[i], "-") {
				project = args[i]
			}
		}
	}

	if dataDir == "" && configPath != "" {
		dataDir = filepath.Join(filepath.Dir(configPath), "data")
	}

	if project == "" {
		project = strings.TrimSpace(os.Getenv("CC_PROJECT"))
	}

	if project == "" {
		return "", "", fmt.Errorf("project name is required (use --project or CC_PROJECT env)")
	}

	return project, dataDir, nil
}

func printStatusUsage() {
	fmt.Println(`Usage: cc-connect status [options] [<project_name>]
       cc-connect status -p <project_name>

Options:
  -p, --project <name>    Seat engine project name (auto-detected from CC_PROJECT env)
  -c, --config <path>     Path to config file
  --data-dir <path>       Explicit path to data directory
  -h, --help              Show this help message`)
}
