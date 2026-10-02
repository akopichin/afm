package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/browser"
	"github.com/akopichin/afm/pkg/config"
)

// newBrowserCmd registers the internal `afm browser` subcommands used by the
// managed Chrome DevTools capability. They are invoked by afm itself (the MCP
// stdio launcher, the host protocol probe) rather than typed by users.
func newBrowserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "browser",
		Short:  "Managed Chrome DevTools browser capability (internal)",
		Hidden: true,
	}
	cmd.AddCommand(newBrowserMCPCmd(), newBrowserStatusCmd(), newBrowserProtocolVersionCmd())
	return cmd
}

func newBrowserMCPCmd() *cobra.Command {
	var descriptor string
	c := &cobra.Command{
		Use:   "mcp",
		Short: "Run the managed Chrome DevTools MCP over stdio (reads --descriptor)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if descriptor == "" {
				return fmt.Errorf("browser mcp: --descriptor is required")
			}
			return browser.RunMCP(cmd.Context(), descriptor, browserRunMode())
		},
	}
	c.Flags().StringVar(&descriptor, "descriptor", "", "path to the per-invocation browser descriptor file (holds the transport token)")
	return c
}

func newBrowserStatusCmd() *cobra.Command {
	var descriptor string
	var asJSON bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Report browser capability status as JSON (never launches Chrome, never prints a credential)",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := browserStatusJSON(descriptor)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		},
	}
	c.Flags().StringVar(&descriptor, "descriptor", "", "path to the browser descriptor file")
	c.Flags().BoolVar(&asJSON, "json", true, "output JSON (always true)")
	return c
}

func newBrowserProtocolVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "protocol-version",
		Short: "Print the browser transport protocol version and exit",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), browserProtocolVersionString())
			return nil
		},
	}
}

// browserRunMode selects docker vs native from the container marker.
func browserRunMode() browser.BrowserMode {
	if config.ReExecedIntoContainer() {
		return browser.BrowserModeDocker
	}
	return browser.BrowserModeNative
}

// browserProtocolVersionString is the compiled-in transport protocol version.
func browserProtocolVersionString() string {
	return strconv.Itoa(browser.TransportProtocolVersion)
}

// browserStatusJSON reports {available, app_url, reason} for an optional
// descriptor path. It NEVER includes the transport token.
func browserStatusJSON(descriptorPath string) (string, error) {
	var st struct {
		Available bool   `json:"available"`
		AppURL    string `json:"app_url"`
		Reason    string `json:"reason,omitempty"`
	}
	switch {
	case descriptorPath == "":
		st.Reason = "browser capability not active for this invocation"
	default:
		d, err := browser.ReadInvocationDescriptor(descriptorPath)
		if err != nil {
			st.Reason = "descriptor unreadable"
		} else {
			st.Available = true
			st.AppURL = d.AppURL
		}
	}
	b, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
