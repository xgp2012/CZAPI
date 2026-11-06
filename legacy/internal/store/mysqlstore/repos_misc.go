package mysqlstore

import (
	"context"
	"database/sql"
	"time"

	"czapi/internal/domain"
	"czapi/internal/pkg/errs"
	"czapi/internal/store"
)

// siteRepo implements store.SiteRepo against MySQL.
type siteRepo struct{ s *Store }

// Create registers a V10 installation.
func (r siteRepo) Create(ctx context.Context, site *domain.Site) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if site == nil || site.AppID == "" {
		return errs.InvalidRequest("site app_id is required")
	}
	now := r.s.now()
	in := *site
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now

	args, err := siteInsertArgs(&in)
	if err != nil {
		return err
	}
	res, err := r.s.db.ExecContext(ctx, siteInsertSQL(), args...)
	if err != nil {
		return wrapDB("mysqlstore: insert site", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrapDB("mysqlstore: site id", err)
	}
	site.ID = uint64(id)
	site.CreatedAt = in.CreatedAt
	site.UpdatedAt = in.UpdatedAt
	return nil
}

// Update replaces an existing site, matched by id.
func (r siteRepo) Update(ctx context.Context, site *domain.Site) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if site == nil || site.ID == 0 {
		return errs.InvalidRequest("site id is required")
	}
	args, err := siteUpdateArgs(site)
	if err != nil {
		return err
	}
	if _, err := r.s.db.ExecContext(ctx, siteUpdateSQL(), args...); err != nil {
		return wrapDB("mysqlstore: update site", err)
	}
	return nil
}

// GetByAppID returns the site registered under appID.
func (r siteRepo) GetByAppID(ctx context.Context, appID string) (*domain.Site, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, siteByAppIDSQL(), []any{appID}, scanSite,
		"mysqlstore: get site by app_id")
}

// scanSite reads one cz_site row in siteColumns order.
func scanSite(rows *sql.Rows) (domain.Site, error) {
	var (
		s       domain.Site
		ipAllow jsonColumn
		enabled bool
	)
	err := rows.Scan(
		&s.ID, &s.AppID, &s.Name, &s.Secret, &s.SecretPrev, &ipAllow, &enabled,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return domain.Site{}, err
	}
	s.Enabled = enabled
	if s.IPAllow, err = ipAllow.decodeSlice(); err != nil {
		return domain.Site{}, err
	}
	return s, nil
}

// List returns every registered site in creation order.
func (r siteRepo) List(ctx context.Context) ([]domain.Site, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scanRows(ctx, r.s.db, siteListSQL(), nil, scanSite)
}

// accountRepo implements store.AccountRepo against MySQL.
type accountRepo struct{ s *Store }

// Upsert creates or updates the account identified by (site_id, v10_client_id)
// and returns the stored row.
//
// A profile sync never moves money: the ON DUPLICATE KEY UPDATE list
// deliberately omits balance_credit.
func (r accountRepo) Upsert(ctx context.Context, a *domain.Account) (*domain.Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a == nil || a.SiteID == "" || a.V10ClientID == 0 {
		return nil, errs.InvalidRequest("account requires site_id and v10_client_id")
	}
	now := r.s.now()
	in := *a
	if in.Status == 0 {
		in.Status = domain.AccountEnabled
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now

	if _, err := r.s.db.ExecContext(ctx, accountUpsertSQL(), accountUpsertArgs(&in)...); err != nil {
		return nil, wrapDB("mysqlstore: upsert account", err)
	}
	stored, err := r.getByClient(ctx, in.SiteID, in.V10ClientID)
	if err != nil {
		return nil, err
	}
	a.ID = stored.ID
	a.BalanceCredit = stored.BalanceCredit
	a.CreatedAt = stored.CreatedAt
	a.UpdatedAt = stored.UpdatedAt
	return stored, nil
}

// scanAccount reads one cz_account row in accountColumns order.
func scanAccount(rows *sql.Rows) (domain.Account, error) {
	var (
		a        domain.Account
		status   int8
		allowNeg bool
	)
	err := rows.Scan(
		&a.ID, &a.SiteID, &a.V10ClientID, &a.Email, &status, &a.BalanceCredit,
		&allowNeg, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return domain.Account{}, err
	}
	a.Status = domain.AccountStatus(status)
	a.AllowNegative = allowNeg
	return a, nil
}

// GetByID returns the account with the given id.
func (r accountRepo) GetByID(ctx context.Context, id uint64) (*domain.Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, accountByIDSQL(), []any{id}, scanAccount,
		"mysqlstore: get account")
}

// GetByV10Client returns the account mirroring a V10 client.
func (r accountRepo) GetByV10Client(ctx context.Context, siteID string, clientID uint64) (*domain.Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.getByClient(ctx, siteID, clientID)
}

// getByClient is GetByV10Client without the context pre-check.
func (r accountRepo) getByClient(ctx context.Context, siteID string, clientID uint64) (*domain.Account, error) {
	return queryOne(ctx, r.s.db, accountByV10ClientSQL(), []any{siteID, clientID}, scanAccount,
		"mysqlstore: get account by v10 client")
}

// Update replaces an existing account, matched by id.
func (r accountRepo) Update(ctx context.Context, a *domain.Account) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || a.ID == 0 {
		return errs.InvalidRequest("account id is required")
	}
	if _, err := r.s.db.ExecContext(ctx, accountUpdateSQL(), accountUpdateArgs(a)...); err != nil {
		return wrapDB("mysqlstore: update account", err)
	}
	return nil
}

// List returns accounts in creation order, paginated.
func (r accountRepo) List(ctx context.Context, offset, limit int) ([]domain.Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q, args := accountListSQL(limit, offset)
	return scanRows(ctx, r.s.db, q, args, scanAccount)
}

// Count returns the number of accounts.
func (r accountRepo) Count(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return countQuery(ctx, r.s.db, accountCountSQL())
}

// planRepo implements store.PlanRepo against MySQL.
type planRepo struct{ s *Store }

// Upsert creates or refreshes a plan identified by its code and returns the
// stored row.
func (r planRepo) Upsert(ctx context.Context, p *domain.Plan) (*domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || p.PlanCode == "" {
		return nil, errs.InvalidRequest("plan_code is required")
	}
	now := r.s.now()
	in := *p
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now

	if _, err := r.s.db.ExecContext(ctx, planUpsertSQL(), planUpsertArgs(&in)...); err != nil {
		return nil, wrapDB("mysqlstore: upsert plan", err)
	}
	stored, err := queryOne(ctx, r.s.db, planByCodeSQL(), []any{in.PlanCode}, scanPlan,
		"mysqlstore: get plan by code")
	if err != nil {
		return nil, err
	}
	p.ID = stored.ID
	p.CreatedAt = stored.CreatedAt
	p.UpdatedAt = stored.UpdatedAt
	return stored, nil
}

// scanPlan reads one cz_plan row in planColumns order.
func scanPlan(rows *sql.Rows) (domain.Plan, error) {
	var p domain.Plan
	err := rows.Scan(
		&p.ID, &p.PlanCode, &p.Name, &p.QuotaTokens, &p.Period, &p.RPMLimit,
		&p.TPMLimit, &p.Concurrency, &p.ModelGroup, &p.MaxKeys, &p.PriceCents,
		&p.GroupRatio, &p.Status, &p.CreatedAt, &p.UpdatedAt,
	)
	return p, err
}

// GetByID returns the plan with the given id.
func (r planRepo) GetByID(ctx context.Context, id uint64) (*domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, planByIDSQL(), []any{id}, scanPlan, "mysqlstore: get plan")
}

// GetByCode returns the plan with the given code.
func (r planRepo) GetByCode(ctx context.Context, code string) (*domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, planByCodeSQL(), []any{code}, scanPlan,
		"mysqlstore: get plan by code")
}

// List returns every plan in creation order.
func (r planRepo) List(ctx context.Context) ([]domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scanRows(ctx, r.s.db, planListSQL(), nil, scanPlan)
}

// channelRepo implements store.ChannelRepo against MySQL.
type channelRepo struct{ s *Store }

// Create stores a new upstream channel.
func (r channelRepo) Create(ctx context.Context, c *domain.Channel) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.Name == "" || c.BaseURL == "" {
		return errs.InvalidRequest("channel requires name and base_url")
	}
	now := r.s.now()
	in := *c
	if in.Provider == "" {
		in.Provider = "openai"
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now

	args, err := channelInsertArgs(&in)
	if err != nil {
		return err
	}
	res, err := r.s.db.ExecContext(ctx, channelInsertSQL(), args...)
	if err != nil {
		return wrapDB("mysqlstore: insert channel", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrapDB("mysqlstore: channel id", err)
	}
	c.ID = uint64(id)
	c.Provider = in.Provider
	c.CreatedAt = in.CreatedAt
	c.UpdatedAt = in.UpdatedAt
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
	args, err := channelUpdateArgs(c)
	if err != nil {
		return err
	}
	if _, err := r.s.db.ExecContext(ctx, channelUpdateSQL(), args...); err != nil {
		return wrapDB("mysqlstore: update channel", err)
	}
	return nil
}

// GetByID returns the channel with the given id.
func (r channelRepo) GetByID(ctx context.Context, id uint64) (*domain.Channel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, channelByIDSQL(), []any{id}, scanChannel,
		"mysqlstore: get channel")
}

// scanChannel reads one cz_channel row in channelColumns order.
func scanChannel(rows *sql.Rows) (domain.Channel, error) {
	var (
		c        domain.Channel
		pool     jsonColumn
		models   jsonColumn
		modelMap jsonColumn
	)
	err := rows.Scan(
		&c.ID, &c.Name, &c.Provider, &c.BaseURL, &pool, &models, &modelMap,
		&c.Weight, &c.Priority, &c.MaxConcurrency, &c.TimeoutMS, &c.Status,
		&c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return domain.Channel{}, err
	}
	keys, err := pool.decodeChannelKeys()
	if err != nil {
		return domain.Channel{}, err
	}
	if len(keys) > 0 {
		c.KeyPool = make([]domain.ChannelKey, 0, len(keys))
		for _, k := range keys {
			c.KeyPool = append(c.KeyPool, domain.ChannelKey{
				Secret:   k.Secret,
				Weight:   k.Weight,
				Status:   k.Status,
				Cooldown: k.Cooldown,
			})
		}
	}
	if c.Models, err = models.decodeSlice(); err != nil {
		return domain.Channel{}, err
	}
	if c.ModelMap, err = modelMap.decodeMap(); err != nil {
		return domain.Channel{}, err
	}
	return c, nil
}

// List returns every channel in creation order.
func (r channelRepo) List(ctx context.Context) ([]domain.Channel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scanRows(ctx, r.s.db, channelListSQL(), nil, scanChannel)
}

// ListByModel returns enabled channels serving the given external model,
// highest priority first.
func (r channelRepo) ListByModel(ctx context.Context, model string) ([]domain.Channel, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.InvalidRequest("model is required")
	}
	return scanRows(ctx, r.s.db, channelListByModelSQL(), []any{model}, scanChannel)
}

// Delete removes a channel and its breaker state.
func (r channelRepo) Delete(ctx context.Context, id uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	res, err := r.s.db.ExecContext(ctx, channelDeleteSQL(), id)
	if err != nil {
		return wrapDB("mysqlstore: delete channel", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return wrapDB("mysqlstore: delete channel rows affected", err)
	}
	if affected == 0 {
		return errs.NotFound("channel not found")
	}
	return nil
}

// GetHealth returns a channel's breaker state. A missing row is reported as
// NotFound; callers treat that as "healthy, no history".
func (r channelRepo) GetHealth(ctx context.Context, channelID uint64) (*domain.ChannelHealth, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, healthGetSQL(), []any{channelID}, scanHealth,
		"mysqlstore: get channel health")
}

// scanHealth reads one cz_channel_health row.
func scanHealth(rows *sql.Rows) (domain.ChannelHealth, error) {
	var (
		h         domain.ChannelHealth
		openUntil sql.NullTime
		halfOpen  bool
	)
	err := rows.Scan(&h.ChannelID, &openUntil, &halfOpen, &h.ErrorRate, &h.Samples, &h.UpdatedAt)
	h.HalfOpen = halfOpen
	if t := fromNullTime(openUntil); t != nil {
		h.OpenUntil = *t
	}
	return h, err
}

// SaveHealth writes a channel's breaker state.
func (r channelRepo) SaveHealth(ctx context.Context, h *domain.ChannelHealth) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h == nil || h.ChannelID == 0 {
		return errs.InvalidRequest("channel_health requires channel_id")
	}
	in := *h
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = r.s.now()
	}
	if _, err := r.s.db.ExecContext(ctx, healthUpsertSQL(), healthUpsertArgs(&in)...); err != nil {
		return wrapDB("mysqlstore: save channel health", err)
	}
	return nil
}

// modelRepo implements store.ModelRepo against MySQL.
type modelRepo struct{ s *Store }

// Upsert creates or refreshes a catalogue entry identified by its model name.
func (r modelRepo) Upsert(ctx context.Context, m *domain.Model) (*domain.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m == nil || m.ModelName == "" {
		return nil, errs.InvalidRequest("model_name is required")
	}
	now := r.s.now()
	in := *m
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now

	args, err := modelUpsertArgs(&in)
	if err != nil {
		return nil, err
	}
	if _, err := r.s.db.ExecContext(ctx, modelUpsertSQL(), args...); err != nil {
		return nil, wrapDB("mysqlstore: upsert model", err)
	}
	stored, err := queryOne(ctx, r.s.db, modelByNameSQL(), []any{in.ModelName}, scanModel,
		"mysqlstore: get model by name")
	if err != nil {
		return nil, err
	}
	m.ID = stored.ID
	m.CreatedAt = stored.CreatedAt
	m.UpdatedAt = stored.UpdatedAt
	return stored, nil
}

// scanModel reads one cz_model row in modelColumns order.
func scanModel(rows *sql.Rows) (domain.Model, error) {
	var (
		m            domain.Model
		capabilities jsonColumn
	)
	err := rows.Scan(
		&m.ID, &m.ModelName, &m.DisplayName, &m.GroupName, &m.ContextLen,
		&m.ModelRatio, &m.CompletionRatio, &m.CachedRatio, &m.InputMicros,
		&m.OutputMicros, &capabilities, &m.Status, &m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		return domain.Model{}, err
	}
	if m.Capabilities, err = capabilities.decodeSlice(); err != nil {
		return domain.Model{}, err
	}
	return m, nil
}

// GetByName returns a catalogue entry by its model name.
func (r modelRepo) GetByName(ctx context.Context, name string) (*domain.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, modelByNameSQL(), []any{name}, scanModel,
		"mysqlstore: get model by name")
}

// List returns the whole catalogue in creation order.
func (r modelRepo) List(ctx context.Context) ([]domain.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scanRows(ctx, r.s.db, modelListSQL(), nil, scanModel)
}

// ListByGroup returns models in one group; "" and "default" both mean "all".
func (r modelRepo) ListByGroup(ctx context.Context, group string) ([]domain.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q, args := modelListByGroupSQL(group)
	return scanRows(ctx, r.s.db, q, args, scanModel)
}

// Delete removes a catalogue entry.
func (r modelRepo) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	res, err := r.s.db.ExecContext(ctx, modelDeleteSQL(), name)
	if err != nil {
		return wrapDB("mysqlstore: delete model", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return wrapDB("mysqlstore: delete model rows affected", err)
	}
	if affected == 0 {
		return errs.NotFound("model not found")
	}
	return nil
}

// adminRepo implements store.AdminRepo against MySQL.
type adminRepo struct{ s *Store }

// Create stores a console operator.
func (r adminRepo) Create(ctx context.Context, a *domain.AdminUser) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || a.Username == "" {
		return errs.InvalidRequest("admin username is required")
	}
	now := r.s.now()
	in := *a
	if in.Role == "" {
		in.Role = "admin"
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now

	res, err := r.s.db.ExecContext(ctx, adminInsertSQL(), adminInsertArgs(&in)...)
	if err != nil {
		return wrapDB("mysqlstore: insert admin", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrapDB("mysqlstore: admin id", err)
	}
	a.ID = uint64(id)
	a.CreatedAt = in.CreatedAt
	a.UpdatedAt = in.UpdatedAt
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
	if _, err := r.s.db.ExecContext(ctx, adminUpdateSQL(), adminUpdateArgs(a)...); err != nil {
		return wrapDB("mysqlstore: update admin", err)
	}
	return nil
}

// GetByID returns the console operator with the given id.
func (r adminRepo) GetByID(ctx context.Context, id uint64) (*domain.AdminUser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, adminByIDSQL(), []any{id}, scanAdmin, "mysqlstore: get admin")
}

// GetByUsername returns the console operator with the given username.
func (r adminRepo) GetByUsername(ctx context.Context, username string) (*domain.AdminUser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, adminByUsernameSQL(), []any{username}, scanAdmin,
		"mysqlstore: get admin by username")
}

// scanAdmin reads one cz_admin_user row in adminColumns order.
func scanAdmin(rows *sql.Rows) (domain.AdminUser, error) {
	var a domain.AdminUser
	err := rows.Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &a.Status,
		&a.CreatedAt, &a.UpdatedAt)
	return a, err
}

// List returns every console operator in creation order.
func (r adminRepo) List(ctx context.Context) ([]domain.AdminUser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scanRows(ctx, r.s.db, adminListSQL(), nil, scanAdmin)
}

// Count returns the number of console operators.
func (r adminRepo) Count(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return countQuery(ctx, r.s.db, adminCountSQL())
}

// auditRepo implements store.AuditRepo against MySQL.
type auditRepo struct{ s *Store }

// Append records an administrative mutation.
func (r auditRepo) Append(ctx context.Context, l *domain.AuditLog) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l == nil || l.Action == "" {
		return errs.InvalidRequest("audit action is required")
	}
	in := *l
	if in.CreatedAt.IsZero() {
		in.CreatedAt = r.s.now()
	}
	res, err := r.s.db.ExecContext(ctx, auditInsertSQL(), auditInsertArgs(&in)...)
	if err != nil {
		return wrapDB("mysqlstore: append audit log", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrapDB("mysqlstore: audit id", err)
	}
	l.ID = uint64(id)
	l.CreatedAt = in.CreatedAt
	return nil
}

// List pages through audit entries, newest first.
func (r auditRepo) List(ctx context.Context, offset, limit int) ([]domain.AuditLog, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q, args := auditListSQL(limit, offset)
	return scanRows(ctx, r.s.db, q, args, func(rows *sql.Rows) (domain.AuditLog, error) {
		var l domain.AuditLog
		err := rows.Scan(&l.ID, &l.ActorType, &l.ActorID, &l.Action, &l.ObjectType,
			&l.ObjectID, &l.Before, &l.After, &l.IP, &l.CreatedAt)
		return l, err
	})
}

// flagRepo implements store.FlagRepo against MySQL.
type flagRepo struct{ s *Store }

// Set writes a feature switch.
func (r flagRepo) Set(ctx context.Context, f *domain.FlagValue) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f == nil || f.Key == "" {
		return errs.InvalidRequest("flag key is required")
	}
	in := *f
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = r.s.now()
	}
	if _, err := r.s.db.ExecContext(ctx, flagUpsertSQL(), flagUpsertArgs(&in)...); err != nil {
		return wrapDB("mysqlstore: set flag", err)
	}
	return nil
}

// scanFlag reads one cz_flag row in flagColumns order.
func scanFlag(rows *sql.Rows) (domain.FlagValue, error) {
	var (
		f     domain.FlagValue
		value bool
	)
	err := rows.Scan(&f.Key, &f.Scope, &value, &f.UpdatedAt)
	f.Value = value
	return f, err
}

// Get returns one feature switch.
func (r flagRepo) Get(ctx context.Context, key, scope string) (*domain.FlagValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryOne(ctx, r.s.db, flagGetSQL(), []any{key, scope}, scanFlag,
		"mysqlstore: get flag")
}

// List returns every feature switch ordered by key then scope.
func (r flagRepo) List(ctx context.Context) ([]domain.FlagValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scanRows(ctx, r.s.db, flagListSQL(), nil, scanFlag)
}

// aggregateRepo implements store.AggregateRepo against MySQL.
type aggregateRepo struct{ s *Store }

// UpsertDaily accumulates d into the rollup identified by
// (day, account, host, key, model).
func (r aggregateRepo) UpsertDaily(ctx context.Context, d *domain.DailyUsage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d == nil || d.Day == "" {
		return errs.InvalidRequest("daily usage day is required")
	}
	if _, err := r.s.db.ExecContext(ctx, dailyUpsertSQL(), dailyUpsertArgs(d)...); err != nil {
		return wrapDB("mysqlstore: upsert daily usage", err)
	}
	return nil
}

// scanDaily reads one cz_daily_usage row in dailyColumns order.
func scanDaily(rows *sql.Rows) (domain.DailyUsage, error) {
	var (
		d   domain.DailyUsage
		err error
	)
	var day string
	err = rows.Scan(&day, &d.AccountID, &d.V10HostID, &d.APIKeyID, &d.Model,
		&d.Requests, &d.PromptTokens, &d.CompletionTokens, &d.CostMicros)
	d.Day = day
	return d, err
}

// ListDaily returns rollups matching f in a deterministic order.
func (r aggregateRepo) ListDaily(ctx context.Context, f store.UsageFilter) ([]domain.DailyUsage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q, args := dailyListSQL(f)
	return scanRows(ctx, r.s.db, q, args, scanDaily)
}

// BucketCost returns one row per day for the trend charts.
func (r aggregateRepo) BucketCost(ctx context.Context, f store.UsageFilter) ([]domain.DailyUsage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q, args := dailyBucketSQL(f)
	return scanRows(ctx, r.s.db, q, args, scanDaily)
}

// RebuildDaily recomputes one UTC day's rollups from the raw usage rows,
// replacing whatever was stored for that day.
//
// It runs in a transaction so a concurrent dashboard read never observes the
// half-rebuilt state.
func (r aggregateRepo) RebuildDaily(ctx context.Context, day time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dayStr := dayString(day)

	tx, err := r.s.begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, dailyDeleteDaySQL(), dayStr); err != nil {
		return wrapDB("mysqlstore: clear daily usage", err)
	}

	// Re-aggregate the raw rows for that day, excluding shadow traffic.
	const q = "SELECT account_id, COALESCE(v10_host_id, 0), api_key_id, model, COUNT(*), " +
		"COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0), " +
		"COALESCE(SUM(cost_micros), 0) FROM cz_usage_record " +
		"WHERE shadow = 0 AND created_at >= ? AND created_at < ? " +
		"GROUP BY account_id, v10_host_id, api_key_id, model"
	start := day.UTC().Truncate(24 * time.Hour)
	rows, err := tx.QueryContext(ctx, q, start, start.AddDate(0, 0, 1))
	if err != nil {
		return wrapDB("mysqlstore: aggregate usage for rebuild", err)
	}
	buckets := make([]domain.DailyUsage, 0, 64)
	for rows.Next() {
		var d domain.DailyUsage
		if err := rows.Scan(&d.AccountID, &d.V10HostID, &d.APIKeyID, &d.Model,
			&d.Requests, &d.PromptTokens, &d.CompletionTokens, &d.CostMicros); err != nil {
			_ = rows.Close()
			return wrapDB("mysqlstore: scan rebuild bucket", err)
		}
		d.Day = dayStr
		buckets = append(buckets, d)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return wrapDB("mysqlstore: iterate rebuild buckets", err)
	}
	if err := rows.Close(); err != nil {
		return wrapDB("mysqlstore: close rebuild rows", err)
	}

	for i := range buckets {
		if _, err := tx.ExecContext(ctx, dailyReplaceSQL(), dailyUpsertArgs(&buckets[i])...); err != nil {
			return wrapDB("mysqlstore: write rebuilt bucket", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return wrapDB("mysqlstore: commit daily rebuild", err)
	}
	committed = true
	return nil
}
