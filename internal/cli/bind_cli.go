package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/gateway"
)

// bindFlags takes --bind session|user (or =value) out of args, sets it
// for the gateway over MAGPIE_BIND, and checks it with what the
// environment had: a value the gateway can't take, from either, refuses
// to start rather than bind otherwise than asked, and so does, bound by
// user, a user-tiers.json that doesn't read (gateway.CheckUserTiers).
// rest is the other arguments.
func bindFlags(args []string) (rest []string, err error) {
	rest, mode, setMode, err := bindingIn(args)
	if err != nil {
		return nil, err
	}
	if setMode {
		os.Setenv(gateway.BindEnv, strings.TrimSpace(mode))
	}
	return rest, nil
}

// bindingIn is bindFlags setting nothing: the other arguments, the mode,
// and whether a flag named it.
func bindingIn(args []string) (rest []string, mode string, setMode bool, err error) {
	mode = os.Getenv(gateway.BindEnv)
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--bind":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return nil, "", false, fmt.Errorf("--bind takes %s or %s: --bind %s", gateway.BindSession, gateway.BindUser, gateway.BindUser)
			}
			i++
			mode, setMode = args[i], true
		case strings.HasPrefix(a, "--bind="):
			mode, setMode = strings.TrimPrefix(a, "--bind="), true
		default:
			rest = append(rest, a)
		}
	}
	b, err := gateway.BindingOf(mode)
	if err != nil {
		return nil, "", false, err
	}
	if b.Mode == gateway.BindUser {
		if err := gateway.CheckUserTiers(); err != nil {
			return nil, "", false, fmt.Errorf("--bind=user: %w; mend it or remove it (without it every user is of the built-in tiers)", err)
		}
	}
	return rest, mode, setMode, nil
}
