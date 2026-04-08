package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var proxyCmd = &cobra.Command{
	Use:   "proxy",
	Short: "Manage tinyproxy domain filter",
}

var proxyAddCmd = &cobra.Command{
	Use:   "add-domain <pattern>",
	Short: "Add a domain pattern to the proxy allow list",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := RequireConfig()
		if err != nil {
			return err
		}
		return addDomainToFilter(cfg.Proxy.FilterFile, args[0])
	},
}

var proxyRemoveCmd = &cobra.Command{
	Use:   "remove-domain <pattern>",
	Short: "Remove a domain pattern from the proxy allow list",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := RequireConfig()
		if err != nil {
			return err
		}
		return removeDomainFromFilter(cfg.Proxy.FilterFile, args[0])
	},
}

var proxyListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show current proxy allow list",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := RequireConfig()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(cfg.Proxy.FilterFile)
		if err != nil {
			return fmt.Errorf("reading filter file: %w", err)
		}
		_, _ = os.Stdout.Write(data)
		return nil
	},
}

func addDomainToFilter(filterFile, pattern string) error {
	data, err := os.ReadFile(filterFile)
	if err != nil {
		return fmt.Errorf("reading filter file: %w", err)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == pattern {
			fmt.Fprintf(os.Stderr, "Pattern %q already in filter file.\n", pattern)
			return nil
		}
	}

	lines = append(lines, pattern)
	return writeFilterFile(filterFile, lines)
}

func removeDomainFromFilter(filterFile, pattern string) error {
	data, err := os.ReadFile(filterFile)
	if err != nil {
		return fmt.Errorf("reading filter file: %w", err)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var kept []string
	found := false
	for _, line := range lines {
		if strings.TrimSpace(line) == pattern {
			found = true
			continue
		}
		kept = append(kept, line)
	}

	if !found {
		fmt.Fprintf(os.Stderr, "Pattern %q not found in filter file.\n", pattern)
		return nil
	}

	return writeFilterFile(filterFile, kept)
}

// writeFilterFile overwrites the filter file in place.
// Existing file permissions are preserved (os.WriteFile only applies perm on create).
func writeFilterFile(filterFile string, lines []string) error {
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filterFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing filter file: %w", err)
	}
	return nil
}

func init() {
	proxyCmd.AddCommand(proxyAddCmd)
	proxyCmd.AddCommand(proxyRemoveCmd)
	proxyCmd.AddCommand(proxyListCmd)
	rootCmd.AddCommand(proxyCmd)
}
