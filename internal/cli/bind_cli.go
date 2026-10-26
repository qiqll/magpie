package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/gateway"
)

// bindFlags takes --bind session|user and --bind-max N (or =value) out of
// args, sets them for the gateway over MAGPIE_BIND and MAGPIE_BIND_MAX,
// and checks them with what the environment had: a value the gateway
// can't take, from either, refuses to start rather than bind otherwise
// than asked. rest is the other arguments.
func bindFlags(args []string) (rest []string, err error) {
	mode, max := os.Getenv(gateway.BindEnv), os.Getenv(gateway.BindMaxEnv)
	setMode, setMax := false, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--bind" || a == "--bind-max":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				if a == "--bind" {
					return nil, fmt.Errorf("--bind takes %s or %s: --bind %s", gateway.BindSession, gateway.BindUser, gateway.BindUser)
				}
				return nil, fmt.Errorf("--bind-max takes a number of accounts: --bind-max 2")
			}
			i++
			if a == "--bind" {
				mode, setMode = args[i], true
			} else {
				max, setMax = args[i], true
			}
		case strings.HasPrefix(a, "--bind="):
			mode, setMode = strings.TrimPrefix(a, "--bind="), true
		case strings.HasPrefix(a, "--bind-max="):
			max, setMax = strings.TrimPrefix(a, "--bind-max="), true
		default:
			rest = append(rest, a)
		}
	}
	if _, err := gateway.BindingOf(mode, max); err != nil {
		return nil, err
	}
	if setMode {
		os.Setenv(gateway.BindEnv, strings.TrimSpace(mode))
	}
	if setMax {
		os.Setenv(gateway.BindMaxEnv, strings.TrimSpace(max))
	}
	return rest, nil
}
