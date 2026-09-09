package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dennisvink/yolomancer/internal/registration"
)

func registrationOptions(o options) (registration.Options, error) {
	var r registration.Options
	if o.profile != "" || o.local || o.baseURL != "" {
		return r, errors.New("register does not accept --profile, --local or --base-url; use --endpoint for a different dispenser")
	}
	for i := 0; i < len(o.args); i++ {
		a := o.args[i]
		if a == "--endpoint" {
			if i+1 >= len(o.args) || strings.HasPrefix(o.args[i+1], "--") {
				return r, fmt.Errorf("%s requires a value", a)
			}
			i++
			r.Endpoint = o.args[i]
		} else {
			if strings.HasPrefix(a, "-") || r.Code != "" {
				return r, errors.New("usage: yolomancer register <CODE> [--endpoint <HTTPS_URL>]")
			}
			r.Code = a
		}
	}
	if r.Code == "" {
		return r, errors.New("usage: yolomancer register <CODE>")
	}
	return r, nil
}

func register(ctx context.Context, o options) error {
	r, err := registrationOptions(o)
	if err != nil {
		return err
	}
	result, err := registration.Register(ctx, r)
	if err != nil {
		return err
	}
	fmt.Printf("Registered %s. Credentials saved to ~/.yolomancer/config.toml.\n", result.AccountUser)
	fmt.Println("Tools will use this account; Bedrock will assume ProsusBedrockAccess. Start with: yolomancer")
	return nil
}
