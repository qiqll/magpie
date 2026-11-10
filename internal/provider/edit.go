package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Edit changes the provider id as change changes it and saves it, checked
// and normalized as Save saves it, but writes only what change changed onto
// the record providers.json has when its lock is taken: a command
// or a TUI key that sets a setting or a few, so that what was written since
// the provider was read (another setting set from the GUI or another
// magpie, an account's day noted, a key added) stays, as Save of the
// provider read would write it over. A provider removed meanwhile isn't
// made again. It answers the provider as read and as changed.
//
// What is written is field by field: a field the change changed is
// written whole, a map or list too (its headers, its windows), so only
// another change to that same field made meanwhile is written over.
func Edit(id string, change func(p *Provider) error) (was, now Provider, err error) {
	p, err := Find(id)
	if err != nil {
		return Provider{}, Provider{}, err
	}
	was, now = p.clone(), p.clone()
	if err := change(&now); err != nil {
		return was, now, err
	}
	return was, now, saveEdit(was, now)
}

// keptStored are the fields magpie keeps of a provider, never one a change
// sets: as the record has them, the picks' list worked out again as Save
// works it out when the picks changed.
var keptStored = []string{"pickedFrom", "delisted", "delistedAt", "accountAdded", "was", "gave"}

// pickFields are those whose change has the picks' list worked out again.
var pickFields = []string{"models", "noModels", "bestInFamily"}

// saveEdit saves p, read as was, writing only the fields of it Save would
// write otherwise than was's.
func saveEdit(was, p Provider) error {
	next, account, err := prepare(p)
	if err != nil {
		return err
	}
	base, _, err := prepare(was)
	if err != nil {
		// the provider read is one Save refuses (put in by hand, a key
		// gone): what was changed can't be told, so it is saved whole, as
		// it was before
		return Save(p)
	}
	changed, err := storedChanges(base, next)
	if err != nil || len(changed) == 0 {
		return err
	}
	var now *listing
	picks := slices.ContainsFunc(pickFields, func(k string) bool { return changed[k] })
	if picks && len(next.Models) > 0 { // listedWith asks it only of picks
		now = listedNow(next) // the accounts are read before the lock
	}
	wrote := false
	err = editRecord(next.ID, account, func(q *Provider) (bool, error) {
		cur := *q
		if !account {
			// as Find reads it and Save writes it
			cur = normalize(cur)
			cur.AccountProxies = keyProxies(cur)
		}
		before, err := json.Marshal(cur)
		if err != nil {
			return false, err
		}
		merged, err := withChanges(cur, next, changed)
		if err != nil {
			return false, err
		}
		if !account {
			merged.AccountProxies = keyProxies(merged)
			// what Save checks of one field against another, on the
			// record as it is now: another change meanwhile can have
			// changed the other (a preset given, Vertex AI picked)
			if err := checkMerged(&merged); err != nil {
				return false, err
			}
		}
		if picks {
			merged.PickedFrom, _ = listedWith(merged, &cur, account, now)
			merged.Delisted = delistedWith(merged, &cur)
			merged.DelistedAt = delistedAtWith(merged, &cur)
		}
		after, err := json.Marshal(merged)
		if err != nil || bytes.Equal(before, after) {
			return false, err
		}
		*q = merged
		wrote = true
		return true, nil
	})
	if err == nil && wrote && next.ID == "claude" {
		syncClaudeProxyEnvNow() // as Save does (#1807)
	}
	return err
}

// checkMerged refuses p as Save would for what one field of it says of
// another, and drops a picture's source from an icon that isn't one, as
// Save does; nothing it checks reads the accounts, so it is made under
// providersMu.
func checkMerged(p *Provider) error {
	if p.IsVertex() && (strings.TrimSpace(p.Key) != "" || len(p.Keys) > 0) {
		return errVertexKey
	}
	if p.InsecureTLS && !p.TakesInsecureTLS() {
		return fmt.Errorf("%s is a preset's: only a provider you added with your own base URL can skip the certificate check", p.Name)
	}
	if !strings.HasPrefix(p.Icon, iconPrefix) {
		p.IconFrom = ""
	}
	return nil
}

// storedChanges are the fields of providers.json's record a and b are
// written with otherwise, but those magpie keeps (keptStored).
func storedChanges(a, b Provider) (map[string]bool, error) {
	x, err := fields(a)
	if err != nil {
		return nil, err
	}
	y, err := fields(b)
	if err != nil {
		return nil, err
	}
	changed := map[string]bool{}
	for k, v := range x {
		if !bytes.Equal(v, y[k]) {
			changed[k] = true
		}
	}
	for k := range y {
		if _, ok := x[k]; !ok {
			changed[k] = true
		}
	}
	for _, k := range keptStored {
		delete(changed, k)
	}
	return changed, nil
}

// withChanges is cur with the fields changed as next has them: one next
// leaves out (cleared) left out too.
func withChanges(cur, next Provider, changed map[string]bool) (Provider, error) {
	m, err := fields(cur)
	if err != nil {
		return Provider{}, err
	}
	n, err := fields(next)
	if err != nil {
		return Provider{}, err
	}
	for k := range changed {
		if v, ok := n[k]; ok {
			m[k] = v
		} else {
			delete(m, k)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return Provider{}, err
	}
	var out Provider
	err = json.Unmarshal(b, &out)
	return out, err
}

// fields are p's fields as providers.json has them, by name.
func fields(p Provider) (map[string]json.RawMessage, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	err = json.Unmarshal(b, &m)
	return m, err
}
