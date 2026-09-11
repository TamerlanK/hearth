package cli

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/TamerlanK/hearth/pkg/client"
	"github.com/spf13/cobra"
)

type dialOpts struct {
	token    string
	useTLS   bool
	insecure bool
	caFile   string
}

func (d *dialOpts) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&d.token, "token", "", "token the server requires; prefer the environment over the command line")
	f.BoolVar(&d.useTLS, "tls", false, "dial with TLS")
	f.BoolVar(&d.insecure, "tls-insecure", false, "with --tls, accept any certificate: for a self-signed server you trust, never over the internet")
	f.StringVar(&d.caFile, "tls-ca", "", "with --tls, PEM file of the certificate authority that signed the server's certificate")
}

func (d *dialOpts) apply(o *client.Options) error {
	o.Token = d.token
	if !d.useTLS {
		if d.insecure || d.caFile != "" {
			return errors.New("--tls-insecure and --tls-ca need --tls")
		}
		return nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: d.insecure}
	if d.caFile != "" {
		pem, err := os.ReadFile(d.caFile)
		if err != nil {
			return fmt.Errorf("read --tls-ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("--tls-ca %s: no certificate found", d.caFile)
		}
		cfg.RootCAs = pool
	}
	o.TLS = cfg
	return nil
}

func serverTLS(certFile, keyFile string) (*tls.Config, error) {
	switch {
	case certFile == "" && keyFile == "":
		return nil, nil
	case certFile == "" || keyFile == "":
		return nil, errors.New("--tls-cert and --tls-key must be given together")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load the tls key pair: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

func dialTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return 10 * time.Second
	}
	return d
}
