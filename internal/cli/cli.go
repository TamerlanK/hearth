package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	Version = "dev"

	Commit = "none"

	Date = "unknown"
)

const envPrefix = "HEARTH_"

func Execute() error {
	err := newRootCmd().ExecuteContext(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "hearth:", err)
	}
	return err
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "hearth",
		Short: "A TCP chat server and terminal client in one binary",
		Long: `Hearth is a small chat system that speaks a line-oriented protocol over TCP.

The same binary runs the server and the client, so one download is enough to
host a room or join one. Every flag can also be set with an environment
variable named after it: --log-format is HEARTH_LOG_FORMAT. Flags win over
the environment, and the environment wins over the built-in default.`,
		Example: `  hearth serve --addr :4000 --metrics-addr :9090
  hearth connect chat.example.com:4000 --name alice`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return applyEnv(cmd.Flags())
		},
	}
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w\n\n%s", err, strings.TrimRight(cmd.UsageString(), "\n"))
	})
	serve, _ := newServeCmd()
	root.AddCommand(serve, newConnectCmd(), newVersionCmd())
	for _, sub := range root.Commands() {
		sub.Flags().VisitAll(func(f *pflag.Flag) {
			f.Usage += " (env: " + envName(f.Name) + ")"
		})
	}
	return root
}

func envName(flag string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(flag, "-", "_"))
}

func applyEnv(fs *pflag.FlagSet) error {
	var err error
	fs.VisitAll(func(f *pflag.Flag) {
		if err != nil || f.Changed || f.Name == "help" {
			return
		}
		name := envName(f.Name)
		val, ok := os.LookupEnv(name)
		if !ok {
			return
		}
		if serr := fs.Set(f.Name, val); serr != nil {
			err = fmt.Errorf("%s=%q: %w", name, val, serr)
		}
	})
	return err
}

type buildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

func newVersionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version, commit, build date and platform",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := buildInfo{Version, Commit, Date, runtime.Version(), runtime.GOOS, runtime.GOARCH}
			w := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			_, err := fmt.Fprintf(w, "hearth %s\n  commit:  %s\n  built:   %s\n  go:      %s\n  os/arch: %s/%s\n",
				info.Version, info.Commit, info.Date, info.Go, info.OS, info.Arch)
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as a JSON object instead of text")
	return cmd
}
