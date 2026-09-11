package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"

	"github.com/spf13/cobra"
)

var errNoServer = errors.New("no server given and none remembered yet; pass host:port")

type remembered struct {
	Addr string `json:"addr"`
	Name string `json:"name"`
}

func configPath(cmd *cobra.Command) (string, error) {
	if path, err := cmd.Flags().GetString("config"); err == nil && path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the user config dir: %w", err)
	}
	return filepath.Join(dir, "hearth", "config.json"), nil
}

func loadRemembered(cmd *cobra.Command) (remembered, error) {
	path, err := configPath(cmd)
	if err != nil {
		return remembered{}, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return remembered{}, nil
	}
	if err != nil {
		return remembered{}, fmt.Errorf("read %s: %w", path, err)
	}
	var r remembered
	if err := json.Unmarshal(b, &r); err != nil {
		return remembered{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return r, nil
}

func saveRemembered(cmd *cobra.Command, r remembered) error {
	path, err := configPath(cmd)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func resolveTarget(cmd *cobra.Command, args []string, name string) (string, string, error) {
	saved, err := loadRemembered(cmd)
	if err != nil {
		return "", "", err
	}
	addr := saved.Addr
	if len(args) > 0 {
		addr = args[0]
	}
	if addr == "" {
		return "", "", errNoServer
	}
	if name == "" {
		name = saved.Name
	}
	if name == "" {
		name = osUserName()
	}
	return addr, name, nil
}

func osUserName() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	if i := len(u.Username) - 1; i >= 0 {
		for j := i; j >= 0; j-- {
			if u.Username[j] == '\\' {
				return u.Username[j+1:]
			}
		}
	}
	return u.Username
}
