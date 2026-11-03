package gateway

import (
	"errors"
	"slices"
	"strings"
)

// unbindTries is how many times Unbind reads the file again when another
// magpie wrote it between its read and its write (errUsersChanged).
const unbindTries = 3

// Unbind lets go of one of a user's accounts of provider pid, by hand
// (the Gateway page's Unbind): the user under every gateway key it is
// bound under, and without one, as the gateway binds it one user
// (sharedOf, T-075); who naming the account as UserRow's Who does, in
// any case. Its first spare is its first when it was the first, and a
// user left with none is bound afresh on its next request (bindUser), as
// when its vendor turns the account away (unbindTurnedAway). found is
// false when the user isn't bound to it: let go meanwhile. The file is
// read again first and written under userBinds, as every change to it is,
// to a file beside it renamed over it (saveUsers), never over a change
// another magpie made since: then it is read again and the change made on
// that. One that doesn't read is an error, and left as it is; one that
// can't be written leaves the binding as the file has it.
func Unbind(pid, user, who string) (found bool, err error) {
	userBinds.Lock()
	defer userBinds.Unlock()
	for try := 1; ; try++ {
		if err := loadUsers(); err != nil {
			return false, err
		}
		if userBinds.stale {
			return false, errors.New("it doesn't read, and is left as it is")
		}
		b, ok := sharedOf("", pid, user)
		if !ok || !seatIn(b.seats(), who) {
			return false, nil
		}
		// each key's binding as it was, for a write that fails
		was := map[string]userBinding{}
		for k, ps := range userBinds.m {
			if kb, has := ps[pid][user]; has {
				was[k] = kb
			}
		}
		putShared(pid, user, slices.DeleteFunc(b.seats(), func(s userSeat) bool { return strings.EqualFold(s.Who, who) }), b.Used)
		for k := range was {
			ps := userBinds.m[k]
			if len(ps[pid]) == 0 {
				delete(ps, pid)
			}
			if len(ps) == 0 {
				delete(userBinds.m, k)
			}
		}
		err := saveUsers()
		if err == nil {
			return true, nil
		}
		// as the file still has it
		for k, kb := range was {
			if userBinds.m == nil {
				userBinds.m = map[string]map[string]map[string]userBinding{}
			}
			if userBinds.m[k] == nil {
				userBinds.m[k] = map[string]map[string]userBinding{}
			}
			if userBinds.m[k][pid] == nil {
				userBinds.m[k][pid] = map[string]userBinding{}
			}
			userBinds.m[k][pid][user] = kb
		}
		if !errors.Is(err, errUsersChanged) || try == unbindTries {
			return false, err
		}
	}
}
