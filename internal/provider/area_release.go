package provider

import (
	"log"
	"strings"

	"github.com/yetone/magpie/internal/area"
)

// releaseArea lets the area that owned an account removed from magpie go
// of it (docs/subsystems/areas.md), however it was removed: the window,
// the command line or an area's admin. ref is "<provider>/<ref>", or
// pid and ref apart. Done only once *err says the removal went through;
// an area that couldn't be told keeps the ref, which lists as gone.
func releaseArea(err *error, pid, ref string) {
	if *err != nil {
		return
	}
	if pid != "" {
		ref = pid + "/" + ref
	}
	if !strings.Contains(ref, "/") {
		return
	}
	if e := area.Release(ref); e != nil {
		log.Printf("areas: %s removed, its area not told: %v", ref, e)
	}
}
