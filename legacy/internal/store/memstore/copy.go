package memstore

import "czapi/internal/domain"

// The helpers below deep-copy the mutable parts of an entity so that neither a
// stored value nor a returned value shares memory with the caller.

// copyStrings returns a copy of in; nil stays nil.
func copyStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// copyStringMap returns a copy of in; nil stays nil.
func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// copyChannelKeys returns a copy of in; nil stays nil.
func copyChannelKeys(in []domain.ChannelKey) []domain.ChannelKey {
	if in == nil {
		return nil
	}
	out := make([]domain.ChannelKey, len(in))
	copy(out, in)
	return out
}

// copySite returns a deep copy of s.
func copySite(s *domain.Site) *domain.Site {
	if s == nil {
		return nil
	}
	out := *s
	out.IPAllow = copyStrings(s.IPAllow)
	return &out
}

// copyAccount returns a copy of a.
func copyAccount(a *domain.Account) *domain.Account {
	if a == nil {
		return nil
	}
	out := *a
	return &out
}

// copyPlan returns a copy of p.
func copyPlan(p *domain.Plan) *domain.Plan {
	if p == nil {
		return nil
	}
	out := *p
	return &out
}

// copySubscription returns a copy of s.
func copySubscription(s *domain.Subscription) *domain.Subscription {
	if s == nil {
		return nil
	}
	out := *s
	return &out
}

// copyAPIKey returns a deep copy of k.
func copyAPIKey(k *domain.APIKey) *domain.APIKey {
	if k == nil {
		return nil
	}
	out := *k
	out.Models = copyStrings(k.Models)
	out.IPWhitelist = copyStrings(k.IPWhitelist)
	if k.ExpiresAt != nil {
		t := *k.ExpiresAt
		out.ExpiresAt = &t
	}
	if k.LastUsedAt != nil {
		t := *k.LastUsedAt
		out.LastUsedAt = &t
	}
	return &out
}

// copyChannel returns a deep copy of c.
func copyChannel(c *domain.Channel) *domain.Channel {
	if c == nil {
		return nil
	}
	out := *c
	out.KeyPool = copyChannelKeys(c.KeyPool)
	out.Models = copyStrings(c.Models)
	out.ModelMap = copyStringMap(c.ModelMap)
	return &out
}

// copyHealth returns a copy of h.
func copyHealth(h *domain.ChannelHealth) *domain.ChannelHealth {
	if h == nil {
		return nil
	}
	out := *h
	return &out
}

// copyModel returns a deep copy of m.
func copyModel(m *domain.Model) *domain.Model {
	if m == nil {
		return nil
	}
	out := *m
	out.Capabilities = copyStrings(m.Capabilities)
	return &out
}

// copyUsage returns a copy of r.
func copyUsage(r *domain.UsageRecord) *domain.UsageRecord {
	if r == nil {
		return nil
	}
	out := *r
	return &out
}

// copyLedgerEntry returns a copy of e.
func copyLedgerEntry(e *domain.CreditLedgerEntry) *domain.CreditLedgerEntry {
	if e == nil {
		return nil
	}
	out := *e
	return &out
}

// copyAdmin returns a copy of a.
func copyAdmin(a *domain.AdminUser) *domain.AdminUser {
	if a == nil {
		return nil
	}
	out := *a
	return &out
}

// copyAudit returns a copy of l.
func copyAudit(l *domain.AuditLog) *domain.AuditLog {
	if l == nil {
		return nil
	}
	out := *l
	return &out
}

// copyFlag returns a copy of f.
func copyFlag(f *domain.FlagValue) *domain.FlagValue {
	if f == nil {
		return nil
	}
	out := *f
	return &out
}

// copyDaily returns a copy of d.
func copyDaily(d *domain.DailyUsage) *domain.DailyUsage {
	if d == nil {
		return nil
	}
	out := *d
	return &out
}
