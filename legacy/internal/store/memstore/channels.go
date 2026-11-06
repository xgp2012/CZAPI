package memstore

import (
	"context"

	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
)

// channelRepo implements store.ChannelRepo on top of Store.
type channelRepo struct{ s *Store }

// Create stores a new channel and assigns its id.
func (r channelRepo) Create(ctx context.Context, c *domain.Channel) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.Name == "" {
		return errs.InvalidRequest("channel name is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	now := r.s.now()
	out := copyChannel(c)
	out.ID = r.s.nextID(kindChannel)
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	out.UpdatedAt = now
	r.s.channels[out.ID] = out
	r.s.channelOrder = append(r.s.channelOrder, out.ID)
	c.ID = out.ID
	c.CreatedAt = out.CreatedAt
	c.UpdatedAt = out.UpdatedAt
	return nil
}

// Update replaces an existing channel, matched by id.
func (r channelRepo) Update(ctx context.Context, c *domain.Channel) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.ID == 0 {
		return errs.InvalidRequest("channel id is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	cur, ok := r.s.channels[c.ID]
	if !ok {
		return errs.NotFound("channel not found")
	}
	out := copyChannel(c)
	out.CreatedAt = cur.CreatedAt
	out.UpdatedAt = r.s.now()
	r.s.channels[out.ID] = out
	return nil
}

// GetByID returns the channel with the given id.
func (r channelRepo) GetByID(ctx context.Context, id uint64) (*domain.Channel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	c, ok := r.s.channels[id]
	if !ok {
		return nil, errs.NotFound("channel not found")
	}
	return copyChannel(c), nil
}

// List returns every channel in creation order.
func (r channelRepo) List(ctx context.Context) ([]domain.Channel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.Channel, 0, len(r.s.channelOrder))
	for _, id := range r.s.channelOrder {
		if c, ok := r.s.channels[id]; ok {
			out = append(out, *copyChannel(c))
		}
	}
	return out, nil
}

// ListByModel returns enabled (Status == 1) channels that serve model.
func (r channelRepo) ListByModel(ctx context.Context, model string) ([]domain.Channel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.Channel, 0, 4)
	for _, id := range r.s.channelOrder {
		c, ok := r.s.channels[id]
		if !ok || c.Status != 1 {
			continue
		}
		if !channelServes(c, model) {
			continue
		}
		out = append(out, *copyChannel(c))
	}
	return out, nil
}

// channelServes reports whether c serves the external model name.
func channelServes(c *domain.Channel, model string) bool {
	for _, m := range c.Models {
		if m == model {
			return true
		}
	}
	if c.ModelMap != nil {
		if _, ok := c.ModelMap[model]; ok {
			return true
		}
	}
	return false
}

// Delete removes a channel and its health record.
func (r channelRepo) Delete(ctx context.Context, id uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	if _, ok := r.s.channels[id]; !ok {
		return errs.NotFound("channel not found")
	}
	delete(r.s.channels, id)
	delete(r.s.health, id)
	for i, v := range r.s.channelOrder {
		if v == id {
			r.s.channelOrder = append(r.s.channelOrder[:i], r.s.channelOrder[i+1:]...)
			break
		}
	}
	return nil
}

// GetHealth returns the breaker state of a channel, or a zero record when the
// channel has no samples yet.
func (r channelRepo) GetHealth(ctx context.Context, channelID uint64) (*domain.ChannelHealth, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	if h, ok := r.s.health[channelID]; ok {
		return copyHealth(h), nil
	}
	if _, ok := r.s.channels[channelID]; !ok {
		return nil, errs.NotFound("channel not found")
	}
	return &domain.ChannelHealth{ChannelID: channelID}, nil
}

// SaveHealth upserts the breaker state of a channel.
func (r channelRepo) SaveHealth(ctx context.Context, h *domain.ChannelHealth) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h == nil || h.ChannelID == 0 {
		return errs.InvalidRequest("channel_id is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	out := copyHealth(h)
	if out.UpdatedAt.IsZero() {
		out.UpdatedAt = r.s.now()
	}
	r.s.health[out.ChannelID] = out
	return nil
}

// modelRepo implements store.ModelRepo on top of Store.
type modelRepo struct{ s *Store }

// Upsert creates or updates a model, keyed by ModelName.
func (r modelRepo) Upsert(ctx context.Context, m *domain.Model) (*domain.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m == nil || m.ModelName == "" {
		return nil, errs.InvalidRequest("model_name is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()

	now := r.s.now()
	out := copyModel(m)
	if cur, ok := r.s.models[m.ModelName]; ok {
		out.ID = cur.ID
		out.CreatedAt = cur.CreatedAt
		out.UpdatedAt = now
		r.s.models[m.ModelName] = out
		return copyModel(out), nil
	}
	out.ID = r.s.nextID(kindModel)
	out.CreatedAt = now
	out.UpdatedAt = now
	r.s.models[out.ModelName] = out
	r.s.modelOrder = append(r.s.modelOrder, out.ModelName)
	return copyModel(out), nil
}

// GetByName returns the model with the given model_name.
func (r modelRepo) GetByName(ctx context.Context, name string) (*domain.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	m, ok := r.s.models[name]
	if !ok {
		return nil, errs.NotFound("model " + name)
	}
	return copyModel(m), nil
}

// List returns every model in creation order.
func (r modelRepo) List(ctx context.Context) ([]domain.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.Model, 0, len(r.s.modelOrder))
	for _, name := range r.s.modelOrder {
		if m, ok := r.s.models[name]; ok {
			out = append(out, *copyModel(m))
		}
	}
	return out, nil
}

// ListByGroup returns the models of one group; "" or "default" means all.
func (r modelRepo) ListByGroup(ctx context.Context, group string) ([]domain.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if group == "" || group == "default" {
		return r.List(ctx)
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.Model, 0, len(r.s.modelOrder))
	for _, name := range r.s.modelOrder {
		m, ok := r.s.models[name]
		if !ok || m.GroupName != group {
			continue
		}
		out = append(out, *copyModel(m))
	}
	return out, nil
}

// Delete removes a model by name.
func (r modelRepo) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	if _, ok := r.s.models[name]; !ok {
		return errs.NotFound("model " + name)
	}
	delete(r.s.models, name)
	for i, v := range r.s.modelOrder {
		if v == name {
			r.s.modelOrder = append(r.s.modelOrder[:i], r.s.modelOrder[i+1:]...)
			break
		}
	}
	return nil
}

// adminRepo implements store.AdminRepo on top of Store.
type adminRepo struct{ s *Store }

// Create stores a new console operator and assigns its id.
func (r adminRepo) Create(ctx context.Context, a *domain.AdminUser) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || a.Username == "" {
		return errs.InvalidRequest("username is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	for _, cur := range r.s.admins {
		if cur.Username == a.Username {
			return errs.Conflict("admin " + a.Username + " already exists")
		}
	}
	now := r.s.now()
	out := copyAdmin(a)
	out.ID = r.s.nextID(kindAdmin)
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	out.UpdatedAt = now
	r.s.admins[out.ID] = out
	r.s.adminOrder = append(r.s.adminOrder, out.ID)
	a.ID = out.ID
	a.CreatedAt = out.CreatedAt
	a.UpdatedAt = out.UpdatedAt
	return nil
}

// Update replaces an existing console operator, matched by id.
func (r adminRepo) Update(ctx context.Context, a *domain.AdminUser) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || a.ID == 0 {
		return errs.InvalidRequest("admin id is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	cur, ok := r.s.admins[a.ID]
	if !ok {
		return errs.NotFound("admin not found")
	}
	out := copyAdmin(a)
	out.CreatedAt = cur.CreatedAt
	out.UpdatedAt = r.s.now()
	r.s.admins[out.ID] = out
	return nil
}

// GetByID returns the console operator with the given id.
func (r adminRepo) GetByID(ctx context.Context, id uint64) (*domain.AdminUser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	a, ok := r.s.admins[id]
	if !ok {
		return nil, errs.NotFound("admin not found")
	}
	return copyAdmin(a), nil
}

// GetByUsername returns the console operator with the given username.
func (r adminRepo) GetByUsername(ctx context.Context, username string) (*domain.AdminUser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	for _, a := range r.s.admins {
		if a.Username == username {
			return copyAdmin(a), nil
		}
	}
	return nil, errs.NotFound("admin " + username)
}

// List returns every console operator in creation order.
func (r adminRepo) List(ctx context.Context) ([]domain.AdminUser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.AdminUser, 0, len(r.s.adminOrder))
	for _, id := range r.s.adminOrder {
		if a, ok := r.s.admins[id]; ok {
			out = append(out, *copyAdmin(a))
		}
	}
	return out, nil
}

// Count returns the number of console operators.
func (r adminRepo) Count(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	return int64(len(r.s.admins)), nil
}

// auditRepo implements store.AuditRepo on top of Store.
type auditRepo struct{ s *Store }

// Append stores an audit entry and assigns its id.
func (r auditRepo) Append(ctx context.Context, l *domain.AuditLog) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l == nil {
		return errs.InvalidRequest("audit log is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	out := copyAudit(l)
	out.ID = r.s.nextID(kindAudit)
	if out.CreatedAt.IsZero() {
		out.CreatedAt = r.s.now()
	}
	r.s.audit = append(r.s.audit, out)
	l.ID = out.ID
	l.CreatedAt = out.CreatedAt
	return nil
}

// List returns audit entries newest first, paginated. A non-positive limit
// returns every remaining row.
func (r auditRepo) List(ctx context.Context, offset, limit int) ([]domain.AuditLog, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.AuditLog, 0, len(r.s.audit))
	seen := 0
	for i := len(r.s.audit) - 1; i >= 0; i-- {
		seen++
		if seen <= offset {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, *copyAudit(r.s.audit[i]))
	}
	return out, nil
}

// flagRepo implements store.FlagRepo on top of Store.
type flagRepo struct{ s *Store }

// flagKey builds the (key, scope) lookup key.
func flagKey(key, scope string) string { return key + "\x00" + scope }

// Set stores a feature switch value for its scope.
func (r flagRepo) Set(ctx context.Context, f *domain.FlagValue) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f == nil || f.Key == "" {
		return errs.InvalidRequest("flag key is required")
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.initMapsLocked()
	out := copyFlag(f)
	if out.UpdatedAt.IsZero() {
		out.UpdatedAt = r.s.now()
	}
	r.s.flags[flagKey(out.Key, out.Scope)] = out
	return nil
}

// Get returns the flag for an exact scope, falling back to the global ("" scope)
// value. It returns errs.NotFound when neither exists.
func (r flagRepo) Get(ctx context.Context, key, scope string) (*domain.FlagValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	if f, ok := r.s.flags[flagKey(key, scope)]; ok {
		return copyFlag(f), nil
	}
	if scope != "" {
		if f, ok := r.s.flags[flagKey(key, "")]; ok {
			return copyFlag(f), nil
		}
	}
	return nil, errs.NotFound("flag " + key)
}

// List returns every stored flag value in unspecified order.
func (r flagRepo) List(ctx context.Context) ([]domain.FlagValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]domain.FlagValue, 0, len(r.s.flags))
	for _, f := range r.s.flags {
		out = append(out, *copyFlag(f))
	}
	return out, nil
}
