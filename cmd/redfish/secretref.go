package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-authn/secretsource"
	"github.com/go-redfish/redfish"
)

// A secret may be named by a REFERENCE (--password-from systemd-creds:bmc-admin)
// instead of a file: github.com/go-authn/secretsource resolves it (a systemd
// credential, a Keychain or Secret Service item, a KeePassXC entry, or a file:
// of mode 0600). The reference holds no secret, so it can sit in argv or a
// script. Only this command imports it: the library stays standard-library only.

// refSecret resolves ref and hands the bytes to use, which must not keep them:
// the secret is wiped when use returns. flag names the option in an error; an
// error never holds the secret, only the reference (which is not one).
func refSecret[T any](ctx context.Context, flag, ref string, use func([]byte) (T, error)) (T, error) {
	var zero T
	s, err := openSecretRef(ctx, ref)
	if err != nil {
		if errors.Is(err, secretsource.ErrTooLarge) {
			return zero, fmt.Errorf("--%s %s: the secret is larger than %d bytes, which a reference cannot carry: use the file or stdin option instead", flag, ref, secretsource.MaxSize)
		}
		return zero, fmt.Errorf("--%s %s: %w", flag, ref, err)
	}
	defer s.Close()
	return use(s.Bytes())
}

// refPassword resolves a one-line secret (the rules of readPassword).
func refPassword(ctx context.Context, flag, ref string) (redfish.Secret, error) {
	return refSecret(ctx, flag, ref, func(b []byte) (redfish.Secret, error) {
		pw, err := readPassword(bytes.NewReader(b))
		if err != nil {
			return "", fmt.Errorf("--%s %s: %w", flag, ref, err)
		}
		return pw, nil
	})
}

// refKey resolves a PEM private key. A reference carries at most
// secretsource.MaxSize bytes, less than redfish.MaxKeyPEM: a longer key is
// refused, and goes through --key-file or --key-stdin.
func refKey(ctx context.Context, flag, ref string) (redfish.Secret, error) {
	return refSecret(ctx, flag, ref, func(b []byte) (redfish.Secret, error) {
		k, err := readKey(bytes.NewReader(b))
		if err != nil {
			return "", fmt.Errorf("--%s %s: %w", flag, ref, err)
		}
		return k, nil
	})
}

// refHeaders resolves the HTTP headers, one "Name: value" per line.
func refHeaders(ctx context.Context, flag, ref string) (map[string]redfish.Secret, error) {
	return refSecret(ctx, flag, ref, func(b []byte) (map[string]redfish.Secret, error) {
		h, err := readHeaders(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("--%s %s: %w", flag, ref, err)
		}
		return h, nil
	})
}

// source is one way to give a secret: an option and whether it was used.
type source struct {
	flag string
	set  bool
}

// exclusive refuses two sources of the same secret.
func exclusive(srcs ...source) error {
	var used []string
	for _, s := range srcs {
		if s.set {
			used = append(used, "--"+s.flag)
		}
	}
	if len(used) < 2 {
		return nil
	}
	return fmt.Errorf("%s are exclusive", strings.Join(used, " and "))
}

// refPasswordGetter is a Credentials that resolves the BMC's password from a
// reference each time it is needed, so it lives as long as a request.
type refPasswordGetter struct{ user, ref string }

func (r refPasswordGetter) Get(ctx context.Context) (string, string, error) {
	pw, err := refPassword(ctx, "password-from", r.ref)
	if err != nil {
		return "", "", err
	}
	return r.user, string(pw), nil
}
